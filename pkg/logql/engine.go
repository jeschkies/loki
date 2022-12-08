package logql

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	//"github.com/grafana/loki/pkg/logqlmodel/metadata"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	promql_parser "github.com/prometheus/prometheus/promql/parser"

	"github.com/grafana/dskit/tenant"

	"github.com/grafana/loki/pkg/iter"
	"github.com/grafana/loki/pkg/logproto"
	"github.com/grafana/loki/pkg/logql/syntax"
	"github.com/grafana/loki/pkg/logqlmodel"
	"github.com/grafana/loki/pkg/logqlmodel/stats"
	"github.com/grafana/loki/pkg/util"
	"github.com/grafana/loki/pkg/util/httpreq"
	logutil "github.com/grafana/loki/pkg/util/log"
	"github.com/grafana/loki/pkg/util/spanlogger"
	"github.com/grafana/loki/pkg/util/validation"
)

const (
	DefaultEngineTimeout       = 5 * time.Minute
	DefaultBlockedQueryMessage = "blocked by policy"
)

var (
	QueryTime = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "logql",
		Name:      "query_duration_seconds",
		Help:      "LogQL query timings",
		Buckets:   prometheus.DefBuckets,
	}, []string{"query_type"})

	QueriesBlocked = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "loki",
		Name:      "blocked_queries",
		Help:      "Count of queries blocked by per-tenant policy",
	}, []string{"user"})

	lastEntryMinTime = time.Unix(-100, 0)
)

type QueryParams interface {
	LogSelector() (syntax.LogSelectorExpr, error)
	GetStart() time.Time
	GetEnd() time.Time
	GetShards() []string
}

// SelectParams specifies parameters passed to data selections.
type SelectLogParams struct {
	*logproto.QueryRequest
}

func (s SelectLogParams) String() string {
	if s.QueryRequest != nil {
		return fmt.Sprintf("selector=%s, direction=%s, start=%s, end=%s, limit=%d, shards=%s",
			s.Selector, logproto.Direction_name[int32(s.Direction)], s.Start, s.End, s.Limit, strings.Join(s.Shards, ","))
	}
	return ""
}

// LogSelector returns the LogSelectorExpr from the SelectParams.
// The `LogSelectorExpr` can then returns all matchers and filters to use for that request.
func (s SelectLogParams) LogSelector() (syntax.LogSelectorExpr, error) {
	return syntax.ParseLogSelector(s.Selector, true)
}

type SelectSampleParams struct {
	*logproto.SampleQueryRequest
}

// Expr returns the SampleExpr from the SelectSampleParams.
// The `LogSelectorExpr` can then returns all matchers and filters to use for that request.
func (s SelectSampleParams) Expr() (syntax.SampleExpr, error) {
	return syntax.ParseSampleExpr(s.Selector)
}

// LogSelector returns the LogSelectorExpr from the SelectParams.
// The `LogSelectorExpr` can then returns all matchers and filters to use for that request.
func (s SelectSampleParams) LogSelector() (syntax.LogSelectorExpr, error) {
	expr, err := syntax.ParseSampleExpr(s.Selector)
	if err != nil {
		return nil, err
	}
	return expr.Selector(), nil
}

// Querier allows a LogQL expression to fetch an EntryIterator for a
// set of matchers and filters
type Querier interface {
	SelectLogs(context.Context, SelectLogParams) (iter.EntryIterator, error)
	SelectSamples(context.Context, SelectSampleParams) (iter.SampleIterator, error)
}

// EngineOpts is the list of options to use with the LogQL query engine.
type EngineOpts struct {
	// TODO: remove this after next release.
	// Timeout for queries execution
	Timeout time.Duration `yaml:"timeout"`

	// MaxLookBackPeriod is the maximum amount of time to look back for log lines.
	// only used for instant log queries.
	MaxLookBackPeriod time.Duration `yaml:"max_look_back_period"`
}

func (opts *EngineOpts) RegisterFlagsWithPrefix(prefix string, f *flag.FlagSet) {
	// TODO: remove this configuration after next release.
	f.DurationVar(&opts.Timeout, prefix+".engine.timeout", DefaultEngineTimeout, "Timeout for query execution. Instead, rely only on querier.query-timeout. (deprecated)")
	f.DurationVar(&opts.MaxLookBackPeriod, prefix+".engine.max-lookback-period", 30*time.Second, "The maximum amount of time to look back for log lines. Used only for instant log queries.")
}

func (opts *EngineOpts) applyDefault() {
	if opts.MaxLookBackPeriod == 0 {
		opts.MaxLookBackPeriod = 30 * time.Second
	}
}

// Engine is the LogQL engine.
type Engine struct {
	Timeout   time.Duration
	logger    log.Logger
	evaluator Evaluator
	limits    Limits
}

// NewEngine creates a new LogQL Engine.
func NewEngine(opts EngineOpts, q Querier, l Limits, logger log.Logger) *Engine {
	queryTimeout := opts.Timeout
	opts.applyDefault()
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &Engine{
		logger:    logger,
		evaluator: NewDefaultEvaluator(q, opts.MaxLookBackPeriod),
		limits:    l,
		Timeout:   queryTimeout,
	}
}

// Query creates a new LogQL query. Instant/Range type is derived from the parameters.
func (ng *Engine) Query(params Params) Query {
	return &query{
		logger:    ng.logger,
		params:    params,
		evaluator: ng.evaluator,
		parse: func(_ context.Context, query string) (syntax.Expr, error) {
			return syntax.ParseExpr(query)
		},
		record: true,
		limits: ng.limits,
	}
}

// Query is a LogQL query to be executed.
type Query interface {
	// Exec processes the query.
	Exec(ctx context.Context) (Iterator, error)
}

type Iterator interface {
	// Returns true if there is more data to iterate and moves the iterator.
	Next() bool
	Error() error
	Current() logqlmodel.Result
	Close() error
}

type SingleItem struct {
	data logqlmodel.Result
	once bool
	err  error
}

func (i SingleItem) Next() bool {
	oldOnce := i.once
	i.once = true
	return oldOnce
}

func (i SingleItem) Current() logqlmodel.Result {
	return i.data
}

func (i SingleItem) Error() error {
	return i.err
}

func (i SingleItem) Close() error {
	return nil
}

type query struct {
	logger    log.Logger
	params    Params
	parse     func(context.Context, string) (syntax.Expr, error)
	limits    Limits
	evaluator Evaluator
	record    bool
}

func (q *query) resultLength(res promql_parser.Value) int {
	switch r := res.(type) {
	case promql.Vector:
		return len(r)
	case promql.Matrix:
		return r.TotalSamples()
	case logqlmodel.Streams:
		return int(r.Lines())
	default:
		// for `scalar` or `string` or any other return type, we just return `0` as result length.
		return 0
	}
}

// Exec Implements `Query`. It handles instrumentation & defers to Eval.
func (q *query) Exec(ctx context.Context) (Iterator, error) {
	log, ctx := spanlogger.New(ctx, "query.Exec")
	defer log.Finish()

	if GetRangeType(q.params) == InstantType {
		level.Info(logutil.WithContext(ctx, q.logger)).Log("msg", "executing query", "type", "instant", "query", q.params.Query())
	} else {
		level.Info(logutil.WithContext(ctx, q.logger)).Log("msg", "executing query", "type", "range", "query", q.params.Query(), "length", q.params.End().Sub(q.params.Start()), "step", q.params.Step())
	}

	rangeType := GetRangeType(q.params)
	timer := prometheus.NewTimer(QueryTime.WithLabelValues(string(rangeType)))
	defer timer.ObserveDuration()

	// records query statistics
	start := time.Now()
	statsCtx, ctx := stats.NewContext(ctx)
	//metadataCtx, ctx := metadata.NewContext(ctx)

	iter, err := q.Eval(ctx)

	queueTime, _ := ctx.Value(httpreq.QueryQueueTimeHTTPHeader).(time.Duration)

	statResult := statsCtx.Result(time.Since(start), queueTime, 0)
	statResult.Log(level.Debug(log))

	/*
			status := "200"
			if err != nil {
				status = "500"
				if errors.Is(err, logqlmodel.ErrParse) ||
					errors.Is(err, logqlmodel.ErrPipeline) ||
					errors.Is(err, logqlmodel.ErrLimit) ||
					errors.Is(err, logqlmodel.ErrBlocked) ||
					errors.Is(err, context.Canceled) {
					status = "400"
				}
			}

			if q.record {
		        		RecordRangeAndInstantQueryMetrics(ctx, q.logger, q.params, status, statResult, data)
			}
	*/

	return iter, err
}

func (q *query) Eval(ctx context.Context) (Iterator, error) {
	tenants, _ := tenant.TenantIDs(ctx)
	queryTimeout := 10 * time.Second//validation.SmallestPositiveNonZeroDurationPerTenant(tenants, q.limits.QueryTimeout)

	ctx, _ = context.WithTimeout(ctx, queryTimeout)
	//defer cancel()

	expr, err := q.parse(ctx, q.params.Query())
	if err != nil {
		return nil, err
	}

	if q.checkBlocked(ctx, tenants) {
		return nil, logqlmodel.ErrBlocked
	}

	switch e := expr.(type) {
	case syntax.SampleExpr:
		value, err := q.evalSample(ctx, e)
		iter := SingleItem{
			data: logqlmodel.Result{Data: value},
			once: false,
			err:  err,
		}
		return iter, err

	case syntax.LogSelectorExpr:
		iter, err := q.evaluator.Iterator(ctx, e, q.params)
		if err != nil {
			return nil, err
		}

		return newStreamsBatchIter(ctx, q.logger, iter, q.params.Limit(), q.params.Direction(), q.params.Interval()), nil
	default:
		return nil, errors.New("Unexpected type (%T): cannot evaluate")
	}
}

func (q *query) checkBlocked(ctx context.Context, tenants []string) bool {
	blocker := newQueryBlocker(ctx, q)

	for _, tenant := range tenants {
		if blocker.isBlocked(tenant) {
			QueriesBlocked.WithLabelValues(tenant).Inc()
			return true
		}
	}

	return false
}

// evalSample evaluate a sampleExpr
func (q *query) evalSample(ctx context.Context, expr syntax.SampleExpr) (promql_parser.Value, error) {
	if lit, ok := expr.(*syntax.LiteralExpr); ok {
		return q.evalLiteral(ctx, lit)
	}
	if vec, ok := expr.(*syntax.VectorExpr); ok {
		return q.evalVector(ctx, vec)
	}

	expr, err := optimizeSampleExpr(expr)
	if err != nil {
		return nil, err
	}

	stepEvaluator, err := q.evaluator.StepEvaluator(ctx, q.evaluator, expr, q.params)
	if err != nil {
		return nil, err
	}
	defer util.LogErrorWithContext(ctx, "closing SampleExpr", stepEvaluator.Close)

	tenantIDs, err := tenant.TenantIDs(ctx)
	if err != nil {
		return nil, err
	}
	maxSeries := validation.SmallestPositiveIntPerTenant(tenantIDs, q.limits.MaxQuerySeries)
	seriesIndex := map[uint64]*promql.Series{}

	next, ts, vec := stepEvaluator.Next()
	if stepEvaluator.Error() != nil {
		return nil, stepEvaluator.Error()
	}

	// fail fast for the first step or instant query
	if len(vec) > maxSeries {
		return nil, logqlmodel.NewSeriesLimitError(maxSeries)
	}

	if GetRangeType(q.params) == InstantType {
		sort.Slice(vec, func(i, j int) bool { return labels.Compare(vec[i].Metric, vec[j].Metric) < 0 })
		return vec, nil
	}

	stepCount := int(math.Ceil(float64(q.params.End().Sub(q.params.Start()).Nanoseconds()) / float64(q.params.Step().Nanoseconds())))
	if stepCount <= 0 {
		stepCount = 1
	}

	for next {
		for _, p := range vec {
			var (
				series *promql.Series
				hash   = p.Metric.Hash()
				ok     bool
			)

			series, ok = seriesIndex[hash]
			if !ok {
				series = &promql.Series{
					Metric: p.Metric,
					Points: make([]promql.Point, 0, stepCount),
				}
				seriesIndex[hash] = series
			}
			series.Points = append(series.Points, promql.Point{
				T: ts,
				V: p.V,
			})
		}
		// as we slowly build the full query for each steps, make sure we don't go over the limit of unique series.
		if len(seriesIndex) > maxSeries {
			return nil, logqlmodel.NewSeriesLimitError(maxSeries)
		}
		next, ts, vec = stepEvaluator.Next()
		if stepEvaluator.Error() != nil {
			return nil, stepEvaluator.Error()
		}
	}

	series := make([]promql.Series, 0, len(seriesIndex))
	for _, s := range seriesIndex {
		series = append(series, *s)
	}
	result := promql.Matrix(series)
	sort.Sort(result)

	return result, stepEvaluator.Error()
}

func (q *query) evalLiteral(_ context.Context, expr *syntax.LiteralExpr) (promql_parser.Value, error) {
	s := promql.Scalar{
		T: q.params.Start().UnixNano() / int64(time.Millisecond),
		V: expr.Value(),
	}

	if GetRangeType(q.params) == InstantType {
		return s, nil
	}

	return PopulateMatrixFromScalar(s, q.params), nil
}

func (q *query) evalVector(_ context.Context, expr *syntax.VectorExpr) (promql_parser.Value, error) {
	value, err := expr.Value()
	if err != nil {
		return nil, err
	}
	s := promql.Scalar{
		T: q.params.Start().UnixNano() / int64(time.Millisecond),
		V: value,
	}

	if GetRangeType(q.params) == InstantType {
		return s, nil
	}

	return PopulateMatrixFromScalar(s, q.params), nil
}

func PopulateMatrixFromScalar(data promql.Scalar, params Params) promql.Matrix {
	var (
		start  = params.Start()
		end    = params.End()
		step   = params.Step()
		series = promql.Series{
			Points: make(
				[]promql.Point,
				0,
				// allocate enough space for all needed entries
				int(end.Sub(start)/step)+1,
			),
		}
	)

	for ts := start; !ts.After(end); ts = ts.Add(step) {
		series.Points = append(series.Points, promql.Point{
			T: ts.UnixNano() / int64(time.Millisecond),
			V: data.V,
		})
	}
	return promql.Matrix{series}
}

func readStreams(i iter.EntryIterator, size uint32, dir logproto.Direction, interval time.Duration) (logqlmodel.Streams, error) {
	streams := map[string]*logproto.Stream{}
	respSize := uint32(0)
	// lastEntry should be a really old time so that the first comparison is always true, we use a negative
	// value here because many unit tests start at time.Unix(0,0)
	lastEntry := lastEntryMinTime
	for respSize < size && i.Next() {
		labels, entry := i.Labels(), i.Entry()
		forwardShouldOutput := dir == logproto.FORWARD &&
			(i.Entry().Timestamp.Equal(lastEntry.Add(interval)) || i.Entry().Timestamp.After(lastEntry.Add(interval)))
		backwardShouldOutput := dir == logproto.BACKWARD &&
			(i.Entry().Timestamp.Equal(lastEntry.Add(-interval)) || i.Entry().Timestamp.Before(lastEntry.Add(-interval)))
		// If step == 0 output every line.
		// If lastEntry.Unix < 0 this is the first pass through the loop and we should output the line.
		// Then check to see if the entry is equal to, or past a forward or reverse step
		if interval == 0 || lastEntry.Unix() < 0 || forwardShouldOutput || backwardShouldOutput {
			stream, ok := streams[labels]
			if !ok {
				stream = &logproto.Stream{
					Labels: labels,
				}
				streams[labels] = stream
			}
			stream.Entries = append(stream.Entries, entry)
			lastEntry = i.Entry().Timestamp
			respSize++
		}
	}

	result := make(logqlmodel.Streams, 0, len(streams))
	for _, stream := range streams {
		result = append(result, *stream)
	}
	sort.Sort(result)
	return result, i.Error()
}

type streamsBatchIter struct {
	ctx     context.Context
	i       iter.EntryIterator
	current logqlmodel.Streams
	err     error
	logger  log.Logger
}

const batchSize = 10

func (i *streamsBatchIter) Next() bool {
	i.current, i.err = readStreams(i.i, batchSize, logproto.BACKWARD, 0)
	next := i.err != nil && len(i.current) != 0
	level.Debug(i.logger).Log("msg", "getting next streams batch", "next", next, "error", i.err, "current", i.current)
	return next
}
func (i *streamsBatchIter) Current() logqlmodel.Result {
	return logqlmodel.Result{
		Data: i.current,
	}
}

func (i *streamsBatchIter) Error() error {
	return i.err
}

func (i *streamsBatchIter) Close() error {
	util.LogErrorWithContext(i.ctx, "closing iterator", i.i.Close)
	return nil
}

func newStreamsBatchIter(ctx context.Context, logger log.Logger, i iter.EntryIterator, size uint32, dir logproto.Direction, interval time.Duration) Iterator {
	return &streamsBatchIter{
		ctx: ctx,
		i:       i,
		current: nil,
		err:     nil,
		logger:  logger,
	}
}

type groupedAggregation struct {
	labels      labels.Labels
	value       float64
	mean        float64
	groupCount  int
	heap        vectorByValueHeap
	reverseHeap vectorByReverseValueHeap
}

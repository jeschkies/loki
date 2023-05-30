package execution

import (
	"sync"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	promql_parser "github.com/prometheus/prometheus/promql/parser"

	"github.com/grafana/loki/pkg/logqlmodel"
)

type RateAggregationEvaluator struct {
	upstream StepEvaluator

	err error
}

func NewRateAggregationEvaluator(upstream StepEvaluator) *RateAggregationEvaluator {
	/*
		vectorAggregator, err := aggregator(expr)
		if err != nil {
			return nil, err
		}
		return &batchRangeVectorIterator{
			iter:     it,
			step:     step,
			end:      end,
			selRange: selRange,
			metrics:  map[string]labels.Labels{},
			window:   map[string]*promql.Series{},
			agg:      vectorAggregator,
			current:  start - step, // first loop iteration will set it to start
			offset:   offset,
		}, nil
	*/
	return &RateAggregationEvaluator{
		upstream: upstream,
	}
}

var _ StepEvaluator = &RateAggregationEvaluator{}

func (r *RateAggregationEvaluator) Next() (ok bool, ts int64, vec promql.Vector) {
	next, ts, vec := r.upstream.Next()
	if !next {
		return false, 0, promql.Vector{}
	}
	for _, s := range vec {
		// Errors are not allowed in metrics unless they've been specifically requested.
		if s.Metric.Has(logqlmodel.ErrorLabel) && s.Metric.Get(logqlmodel.PreserveErrorLabel) != "true" {
			r.err = logqlmodel.NewPipelineErr(s.Metric)
			return false, 0, promql.Vector{}
		}
	}
	return true, ts, vec
}

func (r *RateAggregationEvaluator) Close() error {
	return r.upstream.Close()
}

func (r *RateAggregationEvaluator) Error() error {
	if r.err != nil {
		return r.err
	}
	return r.upstream.Error()
}

// rateLogs calculates the per-second rate of log lines or values extracted
// from log lines
func rateLogs(selRange time.Duration, computeValues bool) func(samples []promql.FPoint) float64 {
	return func(samples []promql.FPoint) float64 {
		if !computeValues {
			return float64(len(samples)) / selRange.Seconds()
		}
		var result float64
		for _, sample := range samples {
			result += sample.F
		}
		return result / selRange.Seconds()
	}
}
//batch

// BatchRangeVectorAggregator aggregates samples for a given range of samples.
// It receives the current milliseconds timestamp and the list of point within
// the range.
type BatchRangeVectorAggregator func([]promql.FPoint) float64

type batchRangeVectorIterator struct {
	upstream                             StepEvaluator
	selRange, step, end, current, offset int64
	window                               map[string]*promql.Series
	metrics                              map[string]labels.Labels
	at                                   []promql.Sample
	agg                                  BatchRangeVectorAggregator
}

func (r *batchRangeVectorIterator) Next() (ok bool, ts int64, vec promql.Vector) {
	// slides the range window to the next position
	r.current = r.current + r.step
	if r.current > r.end {
		return false, 0, promql.Vector{}
	}
	rangeEnd := r.current
	rangeStart := rangeEnd - r.selRange
	// load samples
	r.popBack(rangeStart)
	r.load(rangeStart, rangeEnd)

	ts, vec = r.At()
	return true, ts, vec
}

func (r *batchRangeVectorIterator) Close() error {
	return r.upstream.Close()
}

func (r *batchRangeVectorIterator) Error() error {
	return r.upstream.Error()
}

// popBack removes all entries out of the current window from the back.
func (r *batchRangeVectorIterator) popBack(newStart int64) {
	// possible improvement: if there is no overlap we can just remove all.
	for fp := range r.window {
		lastPoint := 0
		remove := false
		for i, p := range r.window[fp].Floats {
			if p.T <= newStart {
				lastPoint = i
				remove = true
				continue
			}
			break
		}
		if remove {
			r.window[fp].Floats = r.window[fp].Floats[lastPoint+1:]
		}
		if len(r.window[fp].Floats) == 0 {
			s := r.window[fp]
			delete(r.window, fp)
			putSeries(s)
		}
	}
}

// load the next sample range window.
func (r *batchRangeVectorIterator) load(start, end int64) {
	for hasNext, ts, value := r.upstream.Next(); hasNext; hasNext, ts, value = r.upstream.Next() {
		if ts > end {
			// not consuming the iterator as this belong to another range.
			return
		}
		// the lower bound of the range is not inclusive
		if ts <= start {
			_, _, _ = r.upstream.Next()
			continue
		}
		// adds the sample.
		lbls := value.
		var series *promql.Series
		var ok bool
		series, ok = r.window[lbs]
		if !ok {
			var metric labels.Labels
			if metric, ok = r.metrics[lbs]; !ok {
				var err error
				metric, err = promql_parser.ParseMetric(lbs)
				if err != nil {
					_ = r.iter.Next()
					continue
				}
				r.metrics[lbs] = metric
			}

			series = getSeries()
			series.Metric = metric
			r.window[lbs] = series
		}
		p := promql.FPoint{
			T: sample.Timestamp,
			F: sample.Value,
		}
		series.Floats = append(series.Floats, p)
		_ = r.iter.Next()
	}
}
func (r *batchRangeVectorIterator) At() (int64, promql.Vector) {
	if r.at == nil {
		r.at = make([]promql.Sample, 0, len(r.window))
	}
	r.at = r.at[:0]
	// convert ts from nano to milli seconds as the iterator work with nanoseconds
	ts := r.current/1e+6 + r.offset/1e+6
	for _, series := range r.window {
		r.at = append(r.at, promql.Sample{
			F:      r.agg(series.Floats),
			T:      ts,
			Metric: series.Metric,
		})
	}
	return ts, r.at
}

var seriesPool sync.Pool

func getSeries() *promql.Series {
	if r := seriesPool.Get(); r != nil {
		s := r.(*promql.Series)
		s.Floats = s.Floats[:0]
		return s
	}
	return &promql.Series{
		Floats: make([]promql.FPoint, 0, 1024),
	}
}

func putSeries(s *promql.Series) {
	seriesPool.Put(s)
}

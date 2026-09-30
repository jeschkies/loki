package log

import (
	"context"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/dustin/go-humanize"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/model/labels"
)

const (
	ConvertBytes    = "bytes"
	ConvertDuration = "duration"
	ConvertFloat    = "float"
)

// LineExtractor extracts a float64 from a log line.
type LineExtractor func([]byte) float64

var (
	CountExtractor LineExtractor = func(_ []byte) float64 { return 1. }
	BytesExtractor LineExtractor = func(line []byte) float64 { return float64(len(line)) }
)

// BatchLineExtractor is LineExtractor's vectorized counterpart: it computes
// a value for every row currently selected in a batch at once, in the same
// order as ArrowBatch.rows()/Selection, instead of one function call per
// row. Used by streamLineSampleExtractor.ProcessBatch when every leading
// stage ran in batch mode, so the per-line Stage.Process/LabelsBuilder
// machinery that count_over_time/bytes_over_time's Process otherwise pays
// for is skipped entirely - not because touching line content is
// expensive (len(line) is already O(1)), but because the per-row
// interface dispatch, LabelsBuilder churn, and (for CountExtractor) the
// []byte materialization itself are.
type BatchLineExtractor func(b *ArrowBatch) []float64

var (
	// CountExtractorBatch needs no data at all: every selected row is worth
	// exactly 1, matching CountExtractor.
	CountExtractorBatch BatchLineExtractor = func(b *ArrowBatch) []float64 {
		values := make([]float64, b.NumRows())
		for i := range values {
			values[i] = 1
		}
		return values
	}

	// BytesExtractorBatch reads row lengths directly off the Arrow string
	// column's offsets, matching len(line) from BytesExtractor, without
	// materializing any line's []byte. offsets[row+1]-offsets[row] is
	// correct regardless of whether the column's offsets are absolute
	// (e.g. if LineColumn is itself a slice of a larger array): the base
	// offset cancels out in the subtraction.
	BytesExtractorBatch BatchLineExtractor = func(b *ArrowBatch) []float64 {
		rows := b.rows()
		offsets := b.LineColumn.ValueOffsets()
		values := make([]float64, len(rows))
		for i, row := range rows {
			values[i] = float64(offsets[row+1] - offsets[row])
		}
		return values
	}
)

// lineExtractorBatch maps the two well-known LineExtractor values to their
// BatchLineExtractor counterpart. LineExtractor is a bare func type,
// embedded directly rather than behind an interface (to keep the common
// per-line path a plain field access), so it has no way to carry this
// itself; comparing function identity via reflect is safe here because
// there are only ever two well-known package-level LineExtractor values.
// Anything else (a hypothetical future custom extractor) simply gets no
// batch fast path, which only costs performance, never correctness:
// ProcessBatch always falls back to the scalar LineExtractor when this
// returns nil.
func lineExtractorBatch(ex LineExtractor) BatchLineExtractor {
	switch reflect.ValueOf(ex).Pointer() {
	case reflect.ValueOf(CountExtractor).Pointer():
		return CountExtractorBatch
	case reflect.ValueOf(BytesExtractor).Pointer():
		return BytesExtractorBatch
	default:
		return nil
	}
}

// SampleExtractor creates StreamSampleExtractor that can extract samples for a given log stream.
type SampleExtractor interface {
	ForStream(labels labels.Labels) StreamSampleExtractor
}

// StreamSampleExtractor extracts at most one sample from a log line.
// A StreamSampleExtractor never mutates the received line.
type StreamSampleExtractor interface {
	BaseLabels() LabelsResult
	// Process extracts the sample for a log line. It returns the zero sample and
	// false when it extracts none. A true result always carries non-nil Labels.
	Process(ts int64, line []byte, structuredMetadata labels.Labels) (ExtractedSample, bool)
	// ProcessString extracts the sample for a log line. It returns the zero sample
	// and false when it extracts none. A true result always carries non-nil Labels.
	ProcessString(ts int64, line string, structuredMetadata labels.Labels) (ExtractedSample, bool)
	ReferencedStructuredMetadata() bool
	// ProcessBatch is ProcessBatch's StreamSampleExtractor counterpart (see
	// StreamPipeline.ProcessBatch): it runs the extractor's stages against b
	// in batch mode where possible, then computes the extracted value for
	// every row still selected, returning the (possibly narrowed) batch
	// alongside a value per row, in the same order as the returned batch's
	// Selection. This is proof-of-concept code, not yet wired into any
	// production caller.
	ProcessBatch(b *ArrowBatch) (*ArrowBatch, []float64)
}

// ExtractedSample is the sample a StreamSampleExtractor derives from a log line.
type ExtractedSample struct {
	Value  float64
	Labels LabelsResult
}

// SampleExtractorWrapper takes an extractor, wraps it is some desired functionality
// and returns a new pipeline
type SampleExtractorWrapper interface {
	Wrap(ctx context.Context, extractor SampleExtractor, query, tenant string) SampleExtractor
}

type lineSampleExtractor struct {
	Stage
	LineExtractor

	// stages is the un-reduced stage list, kept alongside the Stage above
	// (which ReduceStages collapses into one opaque closure) so
	// ProcessBatch can dispatch on each stage's BatchProcessor support
	// individually, the same way StreamPipeline.ProcessBatch does.
	stages         []Stage
	batchExtractor BatchLineExtractor // nil if ex has no known vectorized counterpart

	baseBuilder      *BaseLabelsBuilder
	streamExtractors map[uint64]StreamSampleExtractor
}

// NewLineSampleExtractor creates a SampleExtractor from a LineExtractor.
// Multiple log stages are run before converting the log line.
func NewLineSampleExtractor(ex LineExtractor, stages []Stage, groups []string, without, noLabels bool) (SampleExtractor, error) {
	s := ReduceStages(stages)
	hints := NewParserHint(s.RequiredLabelNames(), groups, without, noLabels, "", stages)
	return &lineSampleExtractor{
		Stage:            s,
		LineExtractor:    ex,
		stages:           stages,
		batchExtractor:   lineExtractorBatch(ex),
		baseBuilder:      NewBaseLabelsBuilderWithGrouping(groups, hints, without, noLabels),
		streamExtractors: make(map[uint64]StreamSampleExtractor),
	}, nil
}

func (l *lineSampleExtractor) ForStream(labels labels.Labels) StreamSampleExtractor {
	hash := l.baseBuilder.Hash(labels)
	if res, ok := l.streamExtractors[hash]; ok {
		return res
	}

	res := &streamLineSampleExtractor{
		Stage:          l.Stage,
		LineExtractor:  l.LineExtractor,
		stages:         l.stages,
		batchExtractor: l.batchExtractor,
		builder:        l.baseBuilder.ForLabels(labels, hash),
	}
	l.streamExtractors[hash] = res
	return res
}

type streamLineSampleExtractor struct {
	Stage
	LineExtractor
	stages         []Stage
	batchExtractor BatchLineExtractor
	builder        *LabelsBuilder
}

// ProcessBatch implements StreamSampleExtractor. It runs the leading run of
// batch-capable stages directly against b (see runBatchCapableStages), and
// as soon as it reaches a stage with no batch support at all, or if every
// stage ran in batch mode but batchExtractor is nil (no known vectorized
// counterpart for this extractor's LineExtractor), it extracts a value for
// each surviving row one at a time via runRemainingStagesAndExtractPerRow -
// still only for however many rows the batch-capable prefix left selected,
// not the original full batch.
func (l *streamLineSampleExtractor) ProcessBatch(b *ArrowBatch) (*ArrowBatch, []float64) {
	b, i := runBatchCapableStages(l.stages, b)
	if b.Empty() {
		return b, nil
	}
	if i == len(l.stages) && l.batchExtractor != nil {
		values := l.batchExtractor(b)
		return materializeBatchLabels(b, l.builder, (*LabelsBuilder).GroupedLabels), values
	}
	return runRemainingStagesAndExtractPerRow(l.stages[i:], b, l.builder, l.LineExtractor)
}

func (l *streamLineSampleExtractor) ReferencedStructuredMetadata() bool {
	return l.builder.referencedStructuredMetadata
}

func (l *streamLineSampleExtractor) Process(ts int64, line []byte, structuredMetadata labels.Labels) (ExtractedSample, bool) {
	l.builder.Reset()
	l.builder.Add(StructuredMetadataLabel, structuredMetadata)

	// short circuit.
	if l.Stage == NoopStage {
		return ExtractedSample{Value: l.LineExtractor(line), Labels: l.builder.GroupedLabels()}, true
	}

	line, ok := l.Stage.Process(ts, line, l.builder)
	if !ok {
		return ExtractedSample{}, false
	}

	return ExtractedSample{Value: l.LineExtractor(line), Labels: l.builder.GroupedLabels()}, true
}

func (l *streamLineSampleExtractor) ProcessString(ts int64, line string, structuredMetadata labels.Labels) (ExtractedSample, bool) {
	// unsafe get bytes since we have the guarantee that the line won't be mutated.
	return l.Process(ts, unsafeGetBytes(line), structuredMetadata)
}

func (l *streamLineSampleExtractor) BaseLabels() LabelsResult { return l.builder.currentResult }

type convertionFn func(value string) (float64, error)

type labelSampleExtractor struct {
	preStage     Stage
	postFilter   Stage
	labelName    string
	conversionFn convertionFn

	baseBuilder      *BaseLabelsBuilder
	streamExtractors map[uint64]StreamSampleExtractor
}

// LabelExtractorWithStages creates a SampleExtractor that will extract metrics from a labels.
// A set of log stage is executed before the conversion. A Filtering stage is executed after the conversion allowing
// to remove sample containing the __error__ label.
func LabelExtractorWithStages(
	labelName, conversion string,
	groups []string, without, noLabels bool,
	preStages []Stage,
	postFilter Stage,
) (SampleExtractor, error) {
	var convFn convertionFn
	switch conversion {
	case ConvertBytes:
		convFn = convertBytes
	case ConvertDuration:
		convFn = convertDuration
	case ConvertFloat:
		convFn = convertFloat
	default:
		return nil, errors.Errorf("unsupported conversion operation %s", conversion)
	}
	if len(groups) == 0 || without {
		without = true
		groups = append(groups, labelName)
		sort.Strings(groups)
	}
	preStage := ReduceStages(preStages)
	hints := NewParserHint(append(preStage.RequiredLabelNames(), postFilter.RequiredLabelNames()...), groups, without, noLabels, labelName, append(preStages, postFilter))
	return &labelSampleExtractor{
		preStage:         preStage,
		conversionFn:     convFn,
		labelName:        labelName,
		postFilter:       postFilter,
		baseBuilder:      NewBaseLabelsBuilderWithGrouping(groups, hints, without, noLabels),
		streamExtractors: make(map[uint64]StreamSampleExtractor),
	}, nil
}

type streamLabelSampleExtractor struct {
	*labelSampleExtractor
	builder *LabelsBuilder
}

func (l *labelSampleExtractor) ReferencedStructuredMetadata() bool {
	return l.baseBuilder.referencedStructuredMetadata
}

func (l *labelSampleExtractor) ForStream(labels labels.Labels) StreamSampleExtractor {
	hash := l.baseBuilder.Hash(labels)
	if res, ok := l.streamExtractors[hash]; ok {
		return res
	}

	res := &streamLabelSampleExtractor{
		labelSampleExtractor: l,
		builder:              l.baseBuilder.ForLabels(labels, hash),
	}
	l.streamExtractors[hash] = res
	return res
}

func (l *streamLabelSampleExtractor) Process(ts int64, line []byte, structuredMetadata labels.Labels) (ExtractedSample, bool) {
	// Apply the pipeline first.
	l.builder.Reset()
	l.builder.Add(StructuredMetadataLabel, structuredMetadata)
	line, ok := l.preStage.Process(ts, line, l.builder)
	if !ok {
		return ExtractedSample{}, false
	}
	// convert the label value.
	var v float64
	stringValue, _ := l.builder.Get(l.labelName)
	if stringValue == "" {
		// NOTE: It's totally fine for log line to not have this particular label.
		// See Issue: https://github.com/grafana/loki/issues/6713
		return ExtractedSample{}, false
	}

	var err error
	v, err = l.conversionFn(stringValue)
	if err != nil {
		l.builder.SetErr(errSampleExtraction)
		l.builder.SetErrorDetails(err.Error())
	}

	// post filters
	if _, ok = l.postFilter.Process(ts, line, l.builder); !ok {
		return ExtractedSample{}, false
	}
	return ExtractedSample{Value: v, Labels: l.builder.GroupedLabels()}, true
}

func (l *streamLabelSampleExtractor) ProcessString(ts int64, line string, structuredMetadata labels.Labels) (ExtractedSample, bool) {
	// unsafe get bytes since we have the guarantee that the line won't be mutated.
	return l.Process(ts, unsafeGetBytes(line), structuredMetadata)
}

func (l *streamLabelSampleExtractor) BaseLabels() LabelsResult { return l.builder.currentResult }

// ProcessBatch implements StreamSampleExtractor. Label-value extraction has
// no batch fast path yet; this just runs Process per row via
// processBatchFallback, correctness-preserving but not accelerated.
func (l *streamLabelSampleExtractor) ProcessBatch(b *ArrowBatch) (*ArrowBatch, []float64) {
	return processBatchFallback(l, b)
}

// NewDistinctValueSampleExtractor hashes the raw string value of a label or
// extracted field into Sample.Value via xxhash64 / Float64frombits. Missing or
// empty values are skipped.
//
// Grouping matches range aggregations: without=true drops only those groups
// (used when grouping is omitted, to keep stream labels minus the counted
// field); noLabels=true is by () and emits one unlabeled series; otherwise
// output labels are restricted to groups.
func NewDistinctValueSampleExtractor(labelName string, stages []Stage, groups []string, without, noLabels bool) (SampleExtractor, error) {
	if labelName == "" {
		return nil, errors.New("distinct value extractor requires a non-empty label name")
	}
	sortedGroups := make([]string, len(groups))
	copy(sortedGroups, groups)
	sort.Strings(sortedGroups)
	preStage := ReduceStages(stages)
	hints := NewParserHint(preStage.RequiredLabelNames(), sortedGroups, without, noLabels, labelName, stages)
	return &distinctValueSampleExtractor{
		preStage:         preStage,
		labelName:        labelName,
		baseBuilder:      NewBaseLabelsBuilderWithGrouping(sortedGroups, hints, without, noLabels),
		streamExtractors: make(map[uint64]StreamSampleExtractor),
	}, nil
}

type distinctValueSampleExtractor struct {
	preStage         Stage
	labelName        string
	baseBuilder      *BaseLabelsBuilder
	streamExtractors map[uint64]StreamSampleExtractor
}

type streamDistinctValueSampleExtractor struct {
	*distinctValueSampleExtractor
	builder *LabelsBuilder
}

func (d *distinctValueSampleExtractor) ForStream(labels labels.Labels) StreamSampleExtractor {
	hash := d.baseBuilder.Hash(labels)
	if res, ok := d.streamExtractors[hash]; ok {
		return res
	}
	res := &streamDistinctValueSampleExtractor{
		distinctValueSampleExtractor: d,
		builder:                      d.baseBuilder.ForLabels(labels, hash),
	}
	d.streamExtractors[hash] = res
	return res
}

func (d *distinctValueSampleExtractor) ReferencedStructuredMetadata() bool {
	return d.baseBuilder.referencedStructuredMetadata
}

func (d *streamDistinctValueSampleExtractor) Process(ts int64, line []byte, structuredMetadata labels.Labels) (ExtractedSample, bool) {
	d.builder.Reset()
	d.builder.Add(StructuredMetadataLabel, structuredMetadata)
	_, ok := d.preStage.Process(ts, line, d.builder)
	if !ok {
		return ExtractedSample{}, false
	}
	stringValue, found := d.builder.Get(d.labelName)
	if !found || stringValue == "" {
		return ExtractedSample{}, false
	}
	h := xxhash.Sum64(unsafeGetBytes(stringValue))
	return ExtractedSample{
		Value:  math.Float64frombits(h),
		Labels: d.builder.GroupedLabels(),
	}, true
}

func (d *streamDistinctValueSampleExtractor) ProcessString(ts int64, line string, structuredMetadata labels.Labels) (ExtractedSample, bool) {
	return d.Process(ts, unsafeGetBytes(line), structuredMetadata)
}

func (d *streamDistinctValueSampleExtractor) BaseLabels() LabelsResult {
	return d.builder.currentResult
}

// ProcessBatch implements StreamSampleExtractor. Distinct-value extraction
// has no batch fast path yet; this just runs Process per row via
// processBatchFallback, correctness-preserving but not accelerated.
func (d *streamDistinctValueSampleExtractor) ProcessBatch(b *ArrowBatch) (*ArrowBatch, []float64) {
	return processBatchFallback(d, b)
}

// NewFilteringSampleExtractor creates a sample extractor where entries from
// the underlying log stream are filtered by pipeline filters before being
// passed to extract samples. Filters are always upstream of the extractor.
func NewFilteringSampleExtractor(f []PipelineFilter, e SampleExtractor) SampleExtractor {
	return &filteringSampleExtractor{
		filters:   f,
		extractor: e,
	}
}

type filteringSampleExtractor struct {
	filters   []PipelineFilter
	extractor SampleExtractor
}

func (p *filteringSampleExtractor) ForStream(labels labels.Labels) StreamSampleExtractor {
	var streamFilters []streamFilter
	for _, f := range p.filters {
		if allMatch(f.Matchers, labels) {
			streamFilters = append(streamFilters, streamFilter{
				start:    f.Start,
				end:      f.End,
				pipeline: f.Pipeline.ForStream(labels),
			})
		}
	}

	return &filteringStreamExtractor{
		filters:   streamFilters,
		extractor: p.extractor.ForStream(labels),
	}
}

type filteringStreamExtractor struct {
	filters   []streamFilter
	extractor StreamSampleExtractor
}

func (sp *filteringStreamExtractor) ReferencedStructuredMetadata() bool {
	return false
}

func (sp *filteringStreamExtractor) BaseLabels() LabelsResult {
	return sp.extractor.BaseLabels()
}

// ProcessBatch implements StreamSampleExtractor. This is a
// correctness-preserving, unoptimized implementation, matching
// filteringStreamPipeline.ProcessBatch's reasoning: deletion/retention
// filtering isn't the target of the batch work, so it just delegates to
// the existing, well-tested Process per row via processBatchFallback.
func (sp *filteringStreamExtractor) ProcessBatch(b *ArrowBatch) (*ArrowBatch, []float64) {
	return processBatchFallback(sp, b)
}

func (sp *filteringStreamExtractor) Process(ts int64, line []byte, structuredMetadata labels.Labels) (ExtractedSample, bool) {
	for _, filter := range sp.filters {
		if ts < filter.start || ts > filter.end {
			continue
		}

		_, _, matches := filter.pipeline.Process(ts, line, structuredMetadata)
		if matches { // When the filter matches, don't run the next step
			return ExtractedSample{}, false
		}
	}

	return sp.extractor.Process(ts, line, structuredMetadata)
}

func (sp *filteringStreamExtractor) ProcessString(ts int64, line string, structuredMetadata labels.Labels) (ExtractedSample, bool) {
	for _, filter := range sp.filters {
		if ts < filter.start || ts > filter.end {
			continue
		}

		_, _, matches := filter.pipeline.ProcessString(ts, line, structuredMetadata)
		if matches { // When the filter matches, don't run the next step
			return ExtractedSample{}, false
		}
	}

	return sp.extractor.ProcessString(ts, line, structuredMetadata)
}

func convertFloat(v string) (float64, error) {
	return strconv.ParseFloat(v, 64)
}

func convertDuration(v string) (float64, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	return d.Seconds(), nil
}

func convertBytes(v string) (float64, error) {
	b, err := humanize.ParseBytes(v)
	if err != nil {
		return 0, err
	}
	return float64(b), nil
}

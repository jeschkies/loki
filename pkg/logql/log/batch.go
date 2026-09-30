package log

import (
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/loki/v3/pkg/arrowfilter"
)

// ArrowBatch holds a run of log lines from a single stream, to be processed by a
// StreamPipeline's batch-capable stages before falling back to line-by-line
// processing for stages that don't support batching.
//
// This is proof-of-concept code: it reuses arrow.RecordBatch (via the
// LineColumn field) rather than a bespoke on-disk encoding, and
// StructuredMetadata is a plain per-row slice rather than columnar, to keep
// scope small. See pkg/arrowfilter for the underlying vectorized search.
type ArrowBatch struct {
	Timestamps         []int64
	LineColumn         *array.String   // one row per entry in Timestamps
	StructuredMetadata []labels.Labels // parallel to Timestamps; entries may be labels.EmptyLabels()

	// Selection lists the indices of rows still under consideration, in
	// ascending order. A nil Selection means all rows are selected.
	Selection []int32

	// Lines and Labels hold the final, possibly-mutated (line, labels) pair
	// for each row in Selection, once a stage without batch support has
	// materialized them. Until then (nil Lines), results can be read
	// directly off LineColumn using StructuredMetadata for labels.
	Lines  [][]byte
	Labels []LabelsResult
}

// NumRows returns the number of rows still selected in b.
func (b *ArrowBatch) NumRows() int {
	if b.Selection != nil {
		return len(b.Selection)
	}
	return b.LineColumn.Len()
}

// Empty reports whether no rows remain selected.
func (b *ArrowBatch) Empty() bool { return b.NumRows() == 0 }

// rows returns the current selection, materializing the identity selection
// if none has been set yet.
func (b *ArrowBatch) rows() []int32 {
	if b.Selection != nil {
		return b.Selection
	}
	rows := make([]int32, b.LineColumn.Len())
	for i := range rows {
		rows[i] = int32(i)
	}
	return rows
}

// withSelection returns a copy of b with its Selection narrowed to sel.
func (b *ArrowBatch) withSelection(sel []int32) *ArrowBatch {
	return &ArrowBatch{
		Timestamps:         b.Timestamps,
		LineColumn:         b.LineColumn,
		StructuredMetadata: b.StructuredMetadata,
		Selection:          sel,
	}
}

// get returns the original (unmutated) timestamp, line, and structured
// metadata for row, by absolute row index into LineColumn/Timestamps.
func (b *ArrowBatch) get(row int32) (ts int64, line []byte, sm labels.Labels) {
	sm = labels.EmptyLabels()
	if b.StructuredMetadata != nil {
		sm = b.StructuredMetadata[row]
	}
	return b.Timestamps[row], unsafeGetBytes(b.LineColumn.Value(int(row))), sm
}

// BatchProcessor is optionally implemented by a Stage that can filter or
// transform a whole ArrowBatch of lines from a single stream at once, without
// needing per-row label context. Stages that mutate labels (parsers, label
// formatters) do not implement this; StreamPipeline falls back to
// line-by-line processing (via Stage.Process) for those instead.
type BatchProcessor interface {
	ProcessBatch(b *ArrowBatch) *ArrowBatch
}

// runSingleStageLineByLine applies a single label-oblivious Stage's Process
// to each row currently selected in b, narrowing the selection accordingly.
// It never materializes b.Lines/b.Labels, since Filterer-backed stages never
// mutate the line or touch labels.
func runSingleStageLineByLine(process func(ts int64, line []byte, lbs *LabelsBuilder) ([]byte, bool), b *ArrowBatch) *ArrowBatch {
	rows := b.rows()
	newSelection := make([]int32, 0, len(rows))
	for _, row := range rows {
		ts, line, _ := b.get(row)
		if _, ok := process(ts, line, nil); ok {
			newSelection = append(newSelection, row)
		}
	}
	return b.withSelection(newSelection)
}

// runBatchCapableStages runs the leading run of stages that implement
// BatchProcessor against b, narrowing b's selection, and stops at the first
// stage with no batch support at all (or once b is empty, whichever comes
// first). It returns the resulting batch and the index of the first stage
// it didn't run in batch mode - callers should treat i == len(stages) as
// "every stage ran in batch mode". Shared by StreamPipeline.ProcessBatch and
// StreamSampleExtractor.ProcessBatch, since both compose the same []Stage.
func runBatchCapableStages(stages []Stage, b *ArrowBatch) (*ArrowBatch, int) {
	i := 0
	for i < len(stages) {
		bp, ok := stages[i].(BatchProcessor)
		if !ok {
			break
		}
		b = bp.ProcessBatch(b)
		if b.Empty() {
			return b, i + 1
		}
		i++
	}
	return b, i
}

// runRemainingStagesPerRow runs stages (and everything conceptually after
// them) one row at a time over b's current selection, exactly as
// streamPipeline.Process already does today, and materializes the final
// (line, labels) result for every row that survives the whole chain. It is
// used once StreamPipeline.ProcessBatch reaches the first stage with no
// batch support at all.
func runRemainingStagesPerRow(stages []Stage, b *ArrowBatch, lbs *LabelsBuilder) *ArrowBatch {
	rows := b.rows()

	newSelection := make([]int32, 0, len(rows))
	lines := make([][]byte, 0, len(rows))
	results := make([]LabelsResult, 0, len(rows))

	for _, row := range rows {
		ts, line, sm := b.get(row)

		lbs.Reset()
		lbs.Add(StructuredMetadataLabel, sm)

		ok := true
		for _, s := range stages {
			line, ok = s.Process(ts, line, lbs)
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}

		newSelection = append(newSelection, row)
		lines = append(lines, line)
		results = append(results, lbs.LabelsResult())
	}

	return &ArrowBatch{
		Timestamps:         b.Timestamps,
		LineColumn:         b.LineColumn,
		StructuredMetadata: b.StructuredMetadata,
		Selection:          newSelection,
		Lines:              lines,
		Labels:             results,
	}
}

// runRemainingStagesAndExtractPerRow is runRemainingStagesPerRow's
// StreamSampleExtractor counterpart: it runs stages one row at a time over
// b's current selection, then applies extract to the surviving line to
// produce a value, instead of materializing the mutated line itself. Used
// by streamLineSampleExtractor.ProcessBatch once it reaches a stage with no
// batch support, and also (with a nil stages slice) for the case where
// every stage ran in batch mode but no BatchLineExtractor is available -
// extraction still only runs once per surviving row, not once per original
// row, since stages already narrowed the selection.
func runRemainingStagesAndExtractPerRow(stages []Stage, b *ArrowBatch, lbs *LabelsBuilder, extract LineExtractor) (*ArrowBatch, []float64) {
	rows := b.rows()

	newSelection := make([]int32, 0, len(rows))
	values := make([]float64, 0, len(rows))
	results := make([]LabelsResult, 0, len(rows))

	for _, row := range rows {
		ts, line, sm := b.get(row)

		lbs.Reset()
		lbs.Add(StructuredMetadataLabel, sm)

		ok := true
		for _, s := range stages {
			line, ok = s.Process(ts, line, lbs)
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}

		newSelection = append(newSelection, row)
		values = append(values, extract(line))
		results = append(results, lbs.GroupedLabels())
	}

	return &ArrowBatch{
		Timestamps:         b.Timestamps,
		LineColumn:         b.LineColumn,
		StructuredMetadata: b.StructuredMetadata,
		Selection:          newSelection,
		Labels:             results,
	}, values
}

// materializeBatchLabels finalizes b's currently selected rows by computing
// each one's LabelsResult via labelsFn (LabelsBuilder.LabelsResult or
// GroupedLabels, depending on the caller), without re-running any stage.
// Used once a run of batch-capable stages has fully processed b (only ever
// narrowing Selection) so that consumers needing per-row Labels - a real
// iter.EntryIterator/iter.SampleIterator's Labels()/StreamHash(), not just
// the throughput comparisons ProcessBatch was originally benchmarked for -
// get the same (line, labels) result running Process per row would have
// produced.
func materializeBatchLabels(b *ArrowBatch, lbs *LabelsBuilder, labelsFn func(*LabelsBuilder) LabelsResult) *ArrowBatch {
	rows := b.rows()

	lines := make([][]byte, len(rows))
	results := make([]LabelsResult, len(rows))
	for i, row := range rows {
		_, line, sm := b.get(row)
		lines[i] = line
		lbs.Reset()
		lbs.Add(StructuredMetadataLabel, sm)
		results[i] = labelsFn(lbs)
	}

	return &ArrowBatch{
		Timestamps:         b.Timestamps,
		LineColumn:         b.LineColumn,
		StructuredMetadata: b.StructuredMetadata,
		Selection:          rows,
		Lines:              lines,
		Labels:             results,
	}
}

// processBatchFallback runs sp.Process for every row currently selected in
// b, for StreamSampleExtractor implementations with no real batch support
// (label/distinct-value extractors, and the deletion-filtering wrapper).
// Correctness-preserving but doesn't save any per-row work; only
// streamLineSampleExtractor has a genuine vectorized fast path today.
func processBatchFallback(sp StreamSampleExtractor, b *ArrowBatch) (*ArrowBatch, []float64) {
	rows := b.rows()

	newSelection := make([]int32, 0, len(rows))
	values := make([]float64, 0, len(rows))
	results := make([]LabelsResult, 0, len(rows))

	for _, row := range rows {
		ts, line, sm := b.get(row)
		sample, ok := sp.Process(ts, line, sm)
		if !ok {
			continue
		}
		newSelection = append(newSelection, row)
		values = append(values, sample.Value)
		results = append(results, sample.Labels)
	}

	return &ArrowBatch{
		Timestamps:         b.Timestamps,
		LineColumn:         b.LineColumn,
		StructuredMetadata: b.StructuredMetadata,
		Selection:          newSelection,
		Labels:             results,
	}, values
}

// containsBatch is a small adapter so containsFilter (and friends) can offer
// a real vectorized fast path via pkg/arrowfilter.
func containsBatch(match []byte, caseInsensitive bool, fallback func(ts int64, line []byte, lbs *LabelsBuilder) ([]byte, bool)) func(*ArrowBatch) *ArrowBatch {
	return func(b *ArrowBatch) *ArrowBatch {
		if caseInsensitive {
			// arrowfilter.Contains is case-sensitive only; fall back.
			return runSingleStageLineByLine(fallback, b)
		}
		return b.withSelection(arrowfilter.Contains(b.LineColumn, match, b.Selection))
	}
}

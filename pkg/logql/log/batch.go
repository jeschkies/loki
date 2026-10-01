package log

import (
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/loki/v3/pkg/arrowfilter"
)

// SymbolTable resolves structured-metadata symbol IDs - as stored in
// ArrowBatch's StructuredMetadataNames/Values columns - back to the
// label names/values they represent. Implemented by chunkenc's
// symbolizer; kept as a narrow interface here so ArrowBatch doesn't
// need to import chunkenc's on-disk symbolization details, just the
// resolution capability carried along with the batch's raw ID
// columns. names and values are parallel, equal-length slices - one
// entry per (name, value) structured-metadata pair for a single row.
type SymbolTable interface {
	LookupIDs(names, values []uint32) (labels.Labels, error)
}

// ArrowBatch holds a run of log lines from a single stream, to be processed by a
// StreamPipeline's batch-capable stages before falling back to line-by-line
// processing for stages that don't support batching.
//
// This is proof-of-concept code: it reuses arrow.RecordBatch (via the
// LineColumn field) rather than a bespoke on-disk encoding. See
// pkg/arrowfilter for the underlying vectorized search.
type ArrowBatch struct {
	Timestamps         []int64
	LineColumn         *array.String   // one row per entry in Timestamps
	StructuredMetadata []labels.Labels // parallel to Timestamps; entries may be labels.EmptyLabels(). Unused when SymbolTable is set - see StructuredMetadataOffsets.

	// TimestampsBuf is Timestamps' backing storage, when Timestamps is a
	// zero-copy view over a pooled allocator buffer (see
	// decodeColumnarBlockToArrowBatch) rather than a plain make()'d slice -
	// nil otherwise. Released alongside LineColumn in Release.
	TimestampsBuf *memory.Buffer

	// StructuredMetadataOffsets/Names/Values represent structured
	// metadata as raw symbol IDs in real, pooled Arrow columns, instead
	// of eagerly resolving each row into its own heap-allocated
	// labels.Labels during decode (see decodeColumnarBlockToArrowBatch
	// and the design discussion this is based on). For row i,
	// StructuredMetadataOffsets.Value(i):StructuredMetadataOffsets.Value(i+1)
	// indexes into the flat Names/Values columns for that row's
	// (name,value) symbol-ID pairs - same offsets+flat-values shape
	// LineColumn itself already uses. Resolution happens lazily, in
	// get(), via SymbolTable, only for rows that reach it. nil (with
	// SymbolTable nil too) when the batch came from a decode path that
	// hasn't been switched over yet - get() falls back to
	// StructuredMetadata in that case.
	StructuredMetadataOffsets *array.Int32
	StructuredMetadataNames   *array.Uint32
	StructuredMetadataValues  *array.Uint32
	SymbolTable               SymbolTable

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

// Release releases b's LineColumn (an Arrow array, refcounted like any
// other) and returns b's Selection to the shared selection pool (see
// arrowfilter.PutSelection) for reuse by a later batch. Callers - the
// per-block iterators that own an ArrowBatch's whole lifecycle - should
// call this once, when they're done with b, instead of releasing
// LineColumn directly: this is also where Selection's own cleanup belongs,
// now that it's pooled rather than always fresh.
//
// b must not be used after calling Release.
func (b *ArrowBatch) Release() {
	if b.LineColumn != nil {
		b.LineColumn.Release()
	}
	if b.TimestampsBuf != nil {
		b.TimestampsBuf.Release()
	}
	if b.StructuredMetadataOffsets != nil {
		b.StructuredMetadataOffsets.Release()
	}
	if b.StructuredMetadataNames != nil {
		b.StructuredMetadataNames.Release()
	}
	if b.StructuredMetadataValues != nil {
		b.StructuredMetadataValues.Release()
	}
	arrowfilter.PutSelection(b.Selection)
	b.Selection = nil
}

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
		Timestamps:                b.Timestamps,
		TimestampsBuf:             b.TimestampsBuf,
		LineColumn:                b.LineColumn,
		StructuredMetadata:        b.StructuredMetadata,
		StructuredMetadataOffsets: b.StructuredMetadataOffsets,
		StructuredMetadataNames:   b.StructuredMetadataNames,
		StructuredMetadataValues:  b.StructuredMetadataValues,
		SymbolTable:               b.SymbolTable,
		Selection:                 sel,
	}
}

// get returns the original (unmutated) timestamp, line, and structured
// metadata for row, by absolute row index into LineColumn/Timestamps.
//
// Structured metadata resolution happens here, lazily, when b came
// from a decode path populating the raw-symbol-ID columns
// (SymbolTable != nil): only rows get() is actually called for pay the
// resolution cost, instead of every row in the batch paying it eagerly
// at decode time. Falls back to the pre-resolved StructuredMetadata
// slice for decode paths that haven't been switched over to the
// columnar representation.
//
// POC caveat: get() has no error return, so a LookupIDs failure here
// silently falls back to EmptyLabels() for that row instead of
// aborting the whole decode the way the eager path's "symbolizer
// lookup: %w" error does today. Acceptable for this POC (LookupIDs
// only errors on a malformed otel label-name translation, essentially
// never hit in practice), but worth revisiting - likely by having
// get() propagate an error - before this goes beyond proof-of-concept.
func (b *ArrowBatch) get(row int32) (ts int64, line []byte, sm labels.Labels) {
	sm = labels.EmptyLabels()
	switch {
	case b.SymbolTable != nil:
		offsets := b.StructuredMetadataOffsets.Int32Values()
		start, end := offsets[row], offsets[row+1]
		if start != end {
			names := b.StructuredMetadataNames.Uint32Values()[start:end]
			values := b.StructuredMetadataValues.Uint32Values()[start:end]
			resolved, err := b.SymbolTable.LookupIDs(names, values)
			if err == nil {
				sm = resolved
			}
		}
	case b.StructuredMetadata != nil:
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
		Timestamps:                b.Timestamps,
		TimestampsBuf:             b.TimestampsBuf,
		LineColumn:                b.LineColumn,
		StructuredMetadata:        b.StructuredMetadata,
		StructuredMetadataOffsets: b.StructuredMetadataOffsets,
		StructuredMetadataNames:   b.StructuredMetadataNames,
		StructuredMetadataValues:  b.StructuredMetadataValues,
		SymbolTable:               b.SymbolTable,
		Selection:                 newSelection,
		Lines:                     lines,
		Labels:                    results,
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
		Timestamps:                b.Timestamps,
		TimestampsBuf:             b.TimestampsBuf,
		LineColumn:                b.LineColumn,
		StructuredMetadata:        b.StructuredMetadata,
		StructuredMetadataOffsets: b.StructuredMetadataOffsets,
		StructuredMetadataNames:   b.StructuredMetadataNames,
		StructuredMetadataValues:  b.StructuredMetadataValues,
		SymbolTable:               b.SymbolTable,
		Selection:                 newSelection,
		Labels:                    results,
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
		Timestamps:                b.Timestamps,
		TimestampsBuf:             b.TimestampsBuf,
		LineColumn:                b.LineColumn,
		StructuredMetadata:        b.StructuredMetadata,
		StructuredMetadataOffsets: b.StructuredMetadataOffsets,
		StructuredMetadataNames:   b.StructuredMetadataNames,
		StructuredMetadataValues:  b.StructuredMetadataValues,
		SymbolTable:               b.SymbolTable,
		Selection:                 rows,
		Lines:                     lines,
		Labels:                    results,
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
		Timestamps:                b.Timestamps,
		TimestampsBuf:             b.TimestampsBuf,
		LineColumn:                b.LineColumn,
		StructuredMetadata:        b.StructuredMetadata,
		StructuredMetadataOffsets: b.StructuredMetadataOffsets,
		StructuredMetadataNames:   b.StructuredMetadataNames,
		StructuredMetadataValues:  b.StructuredMetadataValues,
		SymbolTable:               b.SymbolTable,
		Selection:                 newSelection,
		Labels:                    results,
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
		old := b.Selection
		newSel := arrowfilter.Contains(b.LineColumn, match, old)
		// old is superseded by newSel and nothing else references it - this
		// *ArrowBatch is about to be discarded by the caller (see
		// runBatchCapableStages, which reassigns its own b to what this
		// returns) - so it's safe to return old to the pool now, one stage
		// before the final result's own Selection gets returned via
		// ArrowBatch.Release.
		arrowfilter.PutSelection(old)
		return b.withSelection(newSel)
	}
}

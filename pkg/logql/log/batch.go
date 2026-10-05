package log

import (
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/model/labels"
)

type ArrowBatch struct {
	Timestamps *array.Int64
	LineColumn *array.String // TODO: use byte array instead
	Samples    *array.Float64

	StructuredMetadata []labels.Labels // TODO: decide on label representation

	// Selection lists the indices of rows still under consideration, in
	// ascending order. A nil Selection means all rows are selected.
	Selection []int
}

func (b *ArrowBatch) NewBuilder() *BatchBuilder {
	builder := &BatchBuilder{
		// TODO: use pooled allocator
		ts:    array.NewInt64Builder(memory.DefaultAllocator),
		lines: array.NewStringBuilder(memory.DefaultAllocator),
	}

	builder.ts.Reserve(len(b.Selection))
	builder.lines.Reserve(len(b.Selection))
	builder.StructuredMetadata = make([]labels.Labels, len(b.Selection), 0)

	return builder
}

type BatchBuilder struct {
	ts    *array.Int64Builder
	lines *array.StringBuilder

	StructuredMetadata []labels.Labels // TODO: decide on label representation
	Selection          []int
}

func (b *BatchBuilder) Append(ts int64, line string, structuredMetadata labels.Labels) {
	b.ts.Append(ts)
	b.lines.Append(line)
	b.StructuredMetadata = append(b.StructuredMetadata, structuredMetadata)
	b.Selection = append(b.Selection, len(b.StructuredMetadata)-1)
}

func (b *BatchBuilder) Result() *ArrowBatch {
	return &ArrowBatch{
		Timestamps:         b.ts.NewInt64Array(),
		LineColumn:         b.lines.NewStringArray(),
		StructuredMetadata: b.StructuredMetadata,
		Selection:          b.Selection,
	}
}

func processLineByLine(b *ArrowBatch, f func(int64, []byte, labels.Labels) (string, LabelsResult, bool)) *ArrowBatch {
	builder := b.NewBuilder()
	for _, i := range b.Selection {
		ts := b.Timestamps.Value(i)
		line, labels, ok := f(ts, unsafeGetBytes(b.LineColumn.Value(i)), b.StructuredMetadata[i])
		if ok {
			// TODO: avoid labels allocation
			builder.Append(ts, line, labels.Labels())
		}
	}
	return builder.Result()
}

func processStageLineByLine(b *ArrowBatch, f func(int64, []byte, *LabelsBuilder) ([]byte, bool)) *ArrowBatch {
	builder := b.NewBuilder()
	for _, i := range b.Selection {
		ts := b.Timestamps.Value(i)
		line, ok := f(ts, unsafeGetBytes(b.LineColumn.Value(i)), nil) // TODO: figure out builder passing
		if ok {
			// TODO: avoid labels allocation
			builder.Append(ts, unsafeGetString(line), labels.EmptyLabels())
		}
	}
	return builder.Result()
}

// NewSampleBuilder creates a SampleBatchBuilder for a batch produced by a
// StreamSampleExtractor. Extractor output has no line, only a numeric sample
// per row, so it uses a dedicated builder instead of BatchBuilder.
func (b *ArrowBatch) NewSampleBuilder() *SampleBatchBuilder {
	builder := &SampleBatchBuilder{
		// TODO: use pooled allocator
		ts:      array.NewInt64Builder(memory.DefaultAllocator),
		samples: array.NewFloat64Builder(memory.DefaultAllocator),
	}

	builder.ts.Reserve(len(b.Selection))
	builder.samples.Reserve(len(b.Selection))
	builder.StructuredMetadata = make([]labels.Labels, 0, len(b.Selection))

	return builder
}

type SampleBatchBuilder struct {
	ts      *array.Int64Builder
	samples *array.Float64Builder

	StructuredMetadata []labels.Labels // TODO: decide on label representation
	Selection          []int
}

func (b *SampleBatchBuilder) Append(ts int64, sample float64, structuredMetadata labels.Labels) {
	b.ts.Append(ts)
	b.samples.Append(sample)
	b.StructuredMetadata = append(b.StructuredMetadata, structuredMetadata)
	b.Selection = append(b.Selection, len(b.StructuredMetadata)-1)
}

func (b *SampleBatchBuilder) Result() *ArrowBatch {
	return &ArrowBatch{
		Timestamps:         b.ts.NewInt64Array(),
		Samples:            b.samples.NewFloat64Array(),
		StructuredMetadata: b.StructuredMetadata,
		Selection:          b.Selection,
	}
}

func processSampleLineByLine(b *ArrowBatch, f func(int64, []byte, labels.Labels) (ExtractedSample, bool)) *ArrowBatch {
	builder := b.NewSampleBuilder()
	for _, i := range b.Selection {
		ts := b.Timestamps.Value(i)
		sample, ok := f(ts, unsafeGetBytes(b.LineColumn.Value(i)), b.StructuredMetadata[i])
		if ok {
			// TODO: avoid labels allocation
			builder.Append(ts, sample.Value, sample.Labels.Labels())
		}
	}
	return builder.Result()
}

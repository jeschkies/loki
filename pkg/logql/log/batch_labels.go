package log

import (
	"slices"
	"strings"
	"sync"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/loki/v3/pkg/util/labelpool"
)

// buildLabelsResult builds the labels of row from its pairs. The row must have at
// least one structured metadata or parsed pair. See LabelsResult.
func (b *ArrowBatch) buildLabelsResult(row int) *columnarLabelsResult {
	scratch := rowLabelsPool.Get().(*rowLabels)
	defer scratch.release()

	// Collect the raw keys and values of the row. They are not turned into Labels
	// first, as that would encode them only to decode them again in the merge.
	var structuredMetadata labels.Labels
	hasMetadataLabels := false
	if b.StructuredMetadataView != nil {
		offs, keys, vals := b.StructuredMetadataColumns()
		scratch.metadata = appendPairs(scratch.metadata, offs, keys, vals, row)
	} else if row < len(b.StructuredMetadata) {
		// The batch has them as labels already, they are used as they are. They are
		// sorted, so they are not sorted again.
		structuredMetadata, hasMetadataLabels = b.StructuredMetadata[row], true
		structuredMetadata.Range(func(l labels.Label) {
			scratch.metadata = append(scratch.metadata, l)
		})
	}
	if b.ParsedLabelsView != nil {
		offs, keys, vals := b.ParsedLabelsColumns()
		scratch.parsed = appendPairs(scratch.parsed, offs, keys, vals, row)
	}

	// The pairs of the columns are in the order of the line, they have to be sorted.
	// They are sorted as labels.Label, not as a larger struct: the comparison of two
	// 32 byte labels is about 2.3 times faster than that of two 40 byte structs.
	// The labels of the stream are sorted already.
	if !hasMetadataLabels {
		slices.SortFunc(scratch.metadata, compareLabelNames)
	}
	slices.SortFunc(scratch.parsed, compareLabelNames)

	stream := labels.EmptyLabels()
	if b.BaseLabels != nil {
		stream = b.BaseLabels.Stream()
	}
	stream.Range(func(l labels.Label) {
		scratch.stream = append(scratch.stream, l)
	})

	// The builders get the labels in order, so they only encode.
	res := &columnarLabelsResult{
		stream:             stream,
		structuredMetadata: structuredMetadata,
		parsed:             buildLabels(scratch.parsed),
	}
	if !hasMetadataLabels {
		res.structuredMetadata = buildLabels(scratch.metadata)
	}
	res.all = mergeSorted(scratch.stream, scratch.metadata, scratch.parsed)
	return res
}

func compareLabelNames(a, b labels.Label) int { return strings.Compare(a.Name, b.Name) }

// buildLabels returns the sorted labels as Labels.
func buildLabels(sorted []labels.Label) labels.Labels {
	if len(sorted) == 0 {
		return labels.EmptyLabels()
	}
	builder := labelpool.Get()
	defer labelpool.Put(builder)
	for _, l := range sorted {
		builder.Add(l.Name, l.Value)
	}
	return builder.Labels()
}

// mergeSorted returns the labels of three sorted lists as one sorted set. If two lists
// have the same name, the label of the first list comes first.
func mergeSorted(a, b, c []labels.Label) labels.Labels {
	builder := labelpool.Get()
	defer labelpool.Put(builder)

	i, j, k := 0, 0, 0
	for i < len(a) || j < len(b) || k < len(c) {
		var l labels.Label
		switch {
		case i < len(a) && (j >= len(b) || a[i].Name <= b[j].Name) && (k >= len(c) || a[i].Name <= c[k].Name):
			l, i = a[i], i+1
		case j < len(b) && (k >= len(c) || b[j].Name <= c[k].Name):
			l, j = b[j], j+1
		default:
			l, k = c[k], k+1
		}
		builder.Add(l.Name, l.Value)
	}
	return builder.Labels()
}

// rowLabels is the scratch space of ArrowBatch.LabelsResult: the labels of the three
// categories of a row.
type rowLabels struct {
	metadata, parsed, stream []labels.Label
}

var rowLabelsPool = sync.Pool{
	New: func() any {
		return &rowLabels{
			metadata: make([]labels.Label, 0, 16),
			parsed:   make([]labels.Label, 0, 32),
			stream:   make([]labels.Label, 0, 16),
		}
	},
}

// release gives the scratch space back to the pool.
func (r *rowLabels) release() {
	// Do not keep the strings of the columns alive.
	clear(r.metadata)
	clear(r.parsed)
	clear(r.stream)
	r.metadata, r.parsed, r.stream = r.metadata[:0], r.parsed[:0], r.stream[:0]
	rowLabelsPool.Put(r)
}

// appendPairs appends the pairs of row to labels. The strings point into the columns.
func appendPairs(pairs []labels.Label, offs []int32, keys, vals *array.StringView, row int) []labels.Label {
	for i := int(offs[row]); i < int(offs[row+1]); i++ {
		pairs = append(pairs, labels.Label{Name: keys.Value(i), Value: vals.Value(i)})
	}
	return pairs
}

// columnarLabelsResult is a LabelsResult built from the columns of a batch.
//
// All labels are built when the result is created, because they come from the
// columns, which may be released afterwards. The string and the hash are only
// computed if they are asked for, not every caller needs them.
type columnarLabelsResult struct {
	stream             labels.Labels
	structuredMetadata labels.Labels
	parsed             labels.Labels
	all                labels.Labels

	strOnce sync.Once
	str     string

	hashOnce sync.Once
	hash     uint64
}

func (l *columnarLabelsResult) String() string {
	l.strOnce.Do(func() { l.str = l.all.String() })
	return l.str
}

func (l *columnarLabelsResult) Labels() labels.Labels { return l.all }

func (l *columnarLabelsResult) Hash() uint64 {
	l.hashOnce.Do(func() {
		// The same hash that LabelsBuilder.LabelsResult computes, over all labels.
		l.hash = newHasher().Hash(l.all)
	})
	return l.hash
}

func (l *columnarLabelsResult) Stream() labels.Labels { return l.stream }

func (l *columnarLabelsResult) StructuredMetadata() labels.Labels { return l.structuredMetadata }

func (l *columnarLabelsResult) Parsed() labels.Labels { return l.parsed }

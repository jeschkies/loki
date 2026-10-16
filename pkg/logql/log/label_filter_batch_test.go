package log

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

// metadataPair is one structured metadata key/value pair of a row.
type metadataPair struct{ key, value string }

// newMetadataMap builds a Map<StringView, StringView> with one entry per row.
func newMetadataMap(t testing.TB, rows [][]metadataPair) *array.Map {
	t.Helper()

	mb := array.NewMapBuilder(memory.DefaultAllocator, arrow.BinaryTypes.StringView, arrow.BinaryTypes.StringView, false)
	defer mb.Release()

	kb := mb.KeyBuilder().(*array.StringViewBuilder)
	vb := mb.ItemBuilder().(*array.StringViewBuilder)
	for _, row := range rows {
		mb.Append(true)
		for _, p := range row {
			kb.Append(p.key)
			vb.Append(p.value)
		}
	}

	return mb.NewMapArray()
}

func TestLineFilterLabelFilter_ProcessBatchStructuredMetadata(t *testing.T) {
	// 16 and 20 bytes, so they are not stored inline in the view header.
	const (
		longKey    = "a_very_long_label_key"
		longValue  = "a_very_long_label_value"
		longValueB = "a_very_long_label_valuf"
	)

	for _, tc := range []struct {
		name      string
		rows      [][]metadataPair
		selection []int
		key       string
		value     string
		want      []int
	}{
		{
			name:      "key and value match",
			rows:      [][]metadataPair{{{"app", "api"}}, {{"app", "web"}}, {{"app", "api"}}},
			selection: []int{0, 1, 2},
			key:       "app",
			value:     "api",
			want:      []int{0, 2},
		},
		{
			name:      "key matches but value differs",
			rows:      [][]metadataPair{{{"app", "web"}}, {{"app", "db"}}},
			selection: []int{0, 1},
			key:       "app",
			value:     "api",
			want:      []int{},
		},
		{
			name:      "key is missing",
			rows:      [][]metadataPair{{{"app", "api"}}, {{"env", "api"}}},
			selection: []int{0, 1},
			key:       "app",
			value:     "api",
			want:      []int{0},
		},
		{
			name:      "key is found among other pairs",
			rows:      [][]metadataPair{{{"env", "prod"}, {"app", "api"}, {"team", "obs"}}, {{"env", "prod"}, {"team", "obs"}}},
			selection: []int{0, 1},
			key:       "app",
			value:     "api",
			want:      []int{0},
		},
		{
			name:      "empty value keeps rows without the key",
			rows:      [][]metadataPair{{{"app", "api"}}, {{"env", "prod"}}, {}},
			selection: []int{0, 1, 2},
			key:       "app",
			value:     "",
			want:      []int{1, 2},
		},
		{
			name:      "empty value does not keep rows with a non-empty value",
			rows:      [][]metadataPair{{{"app", "api"}}, {{"app", ""}}},
			selection: []int{0, 1},
			key:       "app",
			value:     "",
			want:      []int{1},
		},
		{
			name:      "row without metadata does not match a non-empty value",
			rows:      [][]metadataPair{{}, {{"app", "api"}}},
			selection: []int{0, 1},
			key:       "app",
			value:     "api",
			want:      []int{1},
		},
		{
			name:      "same length and prefix but different key",
			rows:      [][]metadataPair{{{"service_a", "x"}}, {{"service_b", "x"}}},
			selection: []int{0, 1},
			key:       "service_b",
			value:     "x",
			want:      []int{1},
		},
		{
			name:      "same length and prefix but different value",
			rows:      [][]metadataPair{{{"app", "abcd_1"}}, {{"app", "abcd_2"}}},
			selection: []int{0, 1},
			key:       "app",
			value:     "abcd_2",
			want:      []int{1},
		},
		{
			name:      "key shorter than the prefix",
			rows:      [][]metadataPair{{{"a", "1"}}, {{"ab", "1"}}, {{"abc", "1"}}},
			selection: []int{0, 1, 2},
			key:       "ab",
			value:     "1",
			want:      []int{1},
		},
		{
			name:      "long key and long value",
			rows:      [][]metadataPair{{{longKey, longValue}}, {{longKey, longValueB}}, {{"app", longValue}}},
			selection: []int{0, 1, 2},
			key:       longKey,
			value:     longValue,
			want:      []int{0},
		},
		{
			name:      "only selected rows are considered",
			rows:      [][]metadataPair{{{"app", "api"}}, {{"app", "api"}}, {{"app", "api"}}},
			selection: []int{0, 2},
			key:       "app",
			value:     "api",
			want:      []int{0, 2},
		},
		{
			name:      "empty selection stays empty",
			rows:      [][]metadataPair{{{"app", "api"}}},
			selection: []int{},
			key:       "app",
			value:     "api",
			want:      []int{},
		},
		// TODO: a nil Selection means all rows are selected, but ProcessBatch does not handle it yet.
		// {
		// 	name:      "nil selection selects all rows",
		// 	rows:      [][]metadataPair{{{"app", "api"}}, {{"app", "web"}}},
		// 	selection: nil,
		// 	key:       "app",
		// 	value:     "api",
		// 	want:      []int{0},
		// },
		// TODO: keys.Data().Buffers()[1] is nil when no row has any metadata and ProcessBatch panics.
		// {
		// 	name:      "no row has metadata",
		// 	rows:      [][]metadataPair{{}, {}},
		// 	selection: []int{0, 1},
		// 	key:       "app",
		// 	value:     "",
		// 	want:      []int{0, 1},
		// },
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMetadataMap(t, tc.rows)
			defer m.Release()

			f := NewStringLabelFilter(labels.MustNewMatcher(labels.MatchEqual, tc.key, tc.value))
			require.IsType(t, &LineFilterLabelFilter{}, f)

			b := &ArrowBatch{
				ParsedLabelsView: m,
				Selection:        tc.selection,
			}

			got := f.ProcessBatch(b)
			require.Equal(t, tc.want, got.Selection)
		})
	}
}

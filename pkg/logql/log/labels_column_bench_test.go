package log

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/loki/v3/pkg/arrowfilter"
)

// buildLogfmtLines builds n synthetic logfmt-shaped lines (the same field
// shape used elsewhere on this branch's benchmarks: level/method/duration),
// as a single Arrow string column.
func buildLogfmtLines(n int) *array.String {
	r := rand.New(rand.NewSource(42))
	bldr := array.NewStringBuilder(memory.DefaultAllocator)
	defer bldr.Release()
	bldr.Reserve(n)

	for i := 0; i < n; i++ {
		line := fmt.Sprintf("level=info method=GET path=/api/v1/query duration=%dms", r.Intn(1000))
		bldr.Append(line)
	}
	return bldr.NewStringArray()
}

// buildLogfmtLinesSelective is buildLogfmtLines with a selectivity knob:
// the fraction of rows whose level is "info" (the filter's target value)
// vs "warn" (a non-matching value of the same length, so Contains'
// candidate-generation cost isn't skewed by length alone).
func buildLogfmtLinesSelective(n int, selectivity float64, seed int64) *array.String {
	r := rand.New(rand.NewSource(seed))
	bldr := array.NewStringBuilder(memory.DefaultAllocator)
	defer bldr.Release()
	bldr.Reserve(n)

	for i := 0; i < n; i++ {
		level := "warn"
		if r.Float64() < selectivity {
			level = "info"
		}
		line := fmt.Sprintf("level=%s method=GET path=/api/v1/query duration=%dms", level, r.Intn(1000))
		bldr.Append(line)
	}
	return bldr.NewStringArray()
}

// BenchmarkLogfmtLabelFilter_RowAtATimeVsColumnar compares the current
// production path (LogfmtParser.Process + StringLabelFilter.Process, one
// row at a time) against BuildLogfmtLabelsColumn + FilterLogfmtLabelsColumn
// (SIMD candidate generation + verify), for `| logfmt | level="info"`, at
// varying batch size and selectivity.
func BenchmarkLogfmtLabelFilter_RowAtATimeVsColumnar(b *testing.B) {
	sizes := []int{256, 4096, 65536}
	selectivities := []float64{1.0, 0.1, 0.0}

	for _, n := range sizes {
		for _, sel := range selectivities {
			lines := buildLogfmtLinesSelective(n, sel, 42)
			name := fmt.Sprintf("rows=%d/selectivity=%.2f", n, sel)

			b.Run(name+"/RowAtATime", func(b *testing.B) {
				parser := NewLogfmtParser(false, false)
				filter := NewStringLabelFilter(labels.MustNewMatcher(labels.MatchEqual, "level", "info"))
				lbs := NewBaseLabelsBuilder().ForLabels(labels.EmptyLabels(), 0)
				b.ReportAllocs()
				for range b.N {
					matched := 0
					for i := 0; i < lines.Len(); i++ {
						lbs.Reset()
						line := unsafeGetBytes(lines.Value(i))
						_, _ = parser.Process(0, line, lbs)
						if _, ok := filter.Process(0, line, lbs); ok {
							matched++
						}
					}
					_ = matched
				}
			})

			b.Run(name+"/ColumnarSIMD", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					col := BuildLogfmtLabelsColumn(logArrowMem, lines)
					rows := FilterLogfmtLabelsColumn(col, []byte("level"), []byte("info"))
					col.Release()
					_ = rows
				}
			})
		}
	}
}

// BenchmarkLogfmtLabelFilter_FullPipeline is the true end-to-end
// comparison: parse/build + filter + materialize the final LabelsResult
// that would actually be handed to an iterator or the metrics engine.
// RowAtATime mirrors real StreamPipeline.Process behavior - LabelsResult is
// only computed for rows that pass the filter, matching how a real
// per-row iterator only calls Labels() after Process returns ok=true, not
// unconditionally for every row.
func BenchmarkLogfmtLabelFilter_FullPipeline(b *testing.B) {
	sizes := []int{256, 4096, 65536}
	selectivities := []float64{1.0, 0.1, 0.0}
	base := labels.FromStrings("app", "bench")

	for _, n := range sizes {
		for _, sel := range selectivities {
			lines := buildLogfmtLinesSelective(n, sel, 42)
			name := fmt.Sprintf("rows=%d/selectivity=%.2f", n, sel)

			b.Run(name+"/RowAtATime-Full", func(b *testing.B) {
				parser := NewLogfmtParser(false, false)
				filter := NewStringLabelFilter(labels.MustNewMatcher(labels.MatchEqual, "level", "info"))
				lbs := NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
				b.ReportAllocs()
				for range b.N {
					matched := 0
					for i := 0; i < lines.Len(); i++ {
						lbs.Reset()
						line := unsafeGetBytes(lines.Value(i))
						_, _ = parser.Process(0, line, lbs)
						if _, ok := filter.Process(0, line, lbs); ok {
							_ = lbs.LabelsResult()
							matched++
						}
					}
					_ = matched
				}
			})

			// RowAtATime-Hinted activates ParserHint machinery that
			// LogfmtParser.Process already has wired in (ShouldExtract,
			// AllRequiredExtracted, ShouldContinueParsingLine) but that
			// NewParserHint never populates for a plain (non-grouped)
			// query - see parser_hints.go:184-186. Constructing the Hints
			// by hand here bypasses that gap to show the ceiling if it
			// were fixed, with zero columnar/SIMD code involved. Like
			// Filtered-Full, its result only has "level" - correct only
			// when nothing else needs another parsed field.
			//
			// Caveat not exercised by this synthetic data: relying only on
			// ShouldContinueParsingLine misses the case where the
			// required key never appears in the line at all (real
			// StringLabelFilter semantics treats a missing label as ""
			// and still filters on it, but ShouldContinueParsingLine is
			// only invoked when the key IS found) - every generated line
			// here always has a level= field, so it never surfaces.
			b.Run(name+"/RowAtATime-Hinted", func(b *testing.B) {
				parser := NewLogfmtParser(false, false)
				filter := NewStringLabelFilter(labels.MustNewMatcher(labels.MatchEqual, "level", "info"))
				hints := &Hints{
					requiredLabels: []string{"level"},
					labelFilters:   []LabelFilterer{filter},
					labelNames:     []string{"level"},
				}
				lbs := NewBaseLabelsBuilderWithGrouping(nil, hints, false, false).ForLabels(base, labels.StableHash(base))
				b.ReportAllocs()
				for range b.N {
					matched := 0
					for i := 0; i < lines.Len(); i++ {
						lbs.Reset()
						line := unsafeGetBytes(lines.Value(i))
						if _, ok := parser.Process(0, line, lbs); ok {
							_ = lbs.LabelsResult()
							matched++
						}
					}
					_ = matched
				}
			})

			b.Run(name+"/ColumnarSIMD-Full", func(b *testing.B) {
				lbs := NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
				b.ReportAllocs()
				for range b.N {
					col := BuildLogfmtLabelsColumn(logArrowMem, lines)
					rows := FilterLogfmtLabelsColumn(col, []byte("level"), []byte("info"))
					for _, row := range rows {
						_ = MaterializeLogfmtLabelsForRow(col, int(row), lbs)
					}
					col.Release()
				}
			})

			// ColumnarSIMD-Filtered-Full only extracts "level" - correct
			// only for a query where nothing downstream needs any other
			// parsed field (e.g. a bare filter with no other reference),
			// mirroring ParserHint.requiredLabels' contract. Its
			// LabelsResult is therefore smaller than Full's (missing
			// method/duration), not just faster to build - this measures
			// what's possible once required-key info reaches the columnar
			// builder, not a drop-in replacement for Full.
			b.Run(name+"/ColumnarSIMD-Filtered-Full", func(b *testing.B) {
				lbs := NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
				wantKeys := [][]byte{[]byte("level")}
				b.ReportAllocs()
				for range b.N {
					col := BuildLogfmtLabelsColumnFiltered(logArrowMem, lines, wantKeys)
					rows := FilterLogfmtLabelsColumn(col, []byte("level"), []byte("info"))
					for _, row := range rows {
						_ = MaterializeLogfmtLabelsForRow(col, int(row), lbs)
					}
					col.Release()
				}
			})

			// StringView-Full: BuildLogfmtValueView + FilterLogfmtValueView,
			// zero-copy against LineColumn's own buffer (short values
			// inline in the ViewHeader, longer ones referencing the
			// buffer directly) - never unescapes, escapes the query
			// literal instead (escapeLogfmtCompareValue). No SIMD: per-row
			// equality, since ViewHeader's layout doesn't fit
			// arrowfilter's buffer-wide scan.
			b.Run(name+"/StringView-Full", func(b *testing.B) {
				lbs := NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
				wantEscaped := escapeLogfmtCompareValue([]byte("info"))
				b.ReportAllocs()
				for range b.N {
					view := BuildLogfmtValueView(logArrowMem, lines, []byte("level"))
					rows := FilterLogfmtValueView(view, wantEscaped)
					for _, row := range rows {
						_ = MaterializeLogfmtValueForRow(view, "level", int(row), lbs)
					}
					arrowfilter.PutSelection(rows)
					view.Release()
				}
			})
		}
	}
}

// BenchmarkLabelsColumn_NaiveVsCurrent compares the naive
// List<Struct<Utf8,Utf8>> columnar builder against the current production
// path (LogfmtParser.Process into a reused LabelsBuilder, one row at a
// time), for the same synthetic logfmt lines. Neither side runs a label
// filter - this isolates the cost of *producing* a logfmt extraction result,
// columnar vs row-at-a-time, before any filtering/SIMD work is layered on.
func BenchmarkLabelsColumn_NaiveVsCurrent(b *testing.B) {
	for _, n := range []int{256, 4096, 65536} {
		lines := buildLogfmtLines(n)

		b.Run(fmt.Sprintf("rows=%d/Current-RowAtATime-WithLabelsResult", n), func(b *testing.B) {
			parser := NewLogfmtParser(false, false)
			lbs := NewBaseLabelsBuilder().ForLabels(labels.EmptyLabels(), 0)
			b.ReportAllocs()
			for range b.N {
				for i := 0; i < lines.Len(); i++ {
					lbs.Reset()
					line := unsafeGetBytes(lines.Value(i))
					_, _ = parser.Process(0, line, lbs)
					_ = lbs.LabelsResult()
				}
			}
		})

		// ExtractionOnly is the fair comparison against Naive-Columnar below:
		// same extraction work (LogfmtParser.Process's Set calls), no final
		// LabelsResult sort/hash/categorize/String build on either side.
		b.Run(fmt.Sprintf("rows=%d/Current-RowAtATime-ExtractionOnly", n), func(b *testing.B) {
			parser := NewLogfmtParser(false, false)
			lbs := NewBaseLabelsBuilder().ForLabels(labels.EmptyLabels(), 0)
			b.ReportAllocs()
			for range b.N {
				for i := 0; i < lines.Len(); i++ {
					lbs.Reset()
					line := unsafeGetBytes(lines.Value(i))
					_, _ = parser.Process(0, line, lbs)
				}
			}
		})

		b.Run(fmt.Sprintf("rows=%d/Naive-Columnar", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				col := BuildLogfmtLabelsColumn(logArrowMem, lines)
				col.Release()
			}
		})
	}
}

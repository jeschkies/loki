package log

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/logql/log/logfmt"
)

// BenchmarkLogfmtParserBatch compares the logfmt stage row by row (Process) with the
// columnar path (ProcessBatch) for `| logfmt`.
//
// The matrix is pairs per line x string length x key kind x share of quoted
// values with escapes.
//
//   - strlen=inline uses keys and values of up to 12 bytes, which fit into the
//     view header. strlen=long uses longer ones, so the headers point into the
//     line column.
//   - keys=plain are valid label names. keys=sanitized contain '-' and '.', so
//     every key is rewritten and lives in the extra buffer.
//   - escaped is the percentage of values that are quoted and contain an escaped
//     quote, so they have to be unescaped into the extra buffer.
//
// Implementations:
//
//   - Process calls StreamPipeline.Process for every line. It sets every pair on
//     the LabelsBuilder and builds the LabelsResult.
//   - ProcessBatch calls StreamPipeline.ProcessBatch once and produces a
//     Map<StringView, StringView>. It does not build any labels.
//   - Scan only runs the logfmt Decoder over the lines. It is the floor: neither
//     of the two other paths can be faster than finding the pairs.
//
// Building the inputs is not timed. Both Process and ProcessBatch are checked
// against each other for a sample of the lines before timing.
func BenchmarkLogfmtParserBatch(b *testing.B) {
	const entries = 10_000
	for _, lengths := range []string{"inline", "long"} {
		for _, keys := range []string{"plain", "sanitized"} {
			for _, numPairs := range []int{5, 20} {
				for _, escapedPct := range []int{0, 10} {
					name := fmt.Sprintf("strlen=%s/keys=%s/pairs=%d/escaped=%d%%", lengths, keys, numPairs, escapedPct)
					b.Run(name, func(b *testing.B) {
						lines := generateLogfmtLines(entries, numPairs, lengths == "long", keys == "sanitized", escapedPct)
						benchmarkLogfmtParserBatch(b, lines)
					})
				}
			}
		}
	}
}

func benchmarkLogfmtParserBatch(b *testing.B, lines []string) {
	entries := len(lines)
	batch := newLogfmtBatch(lines)
	batch.Selection = allRows(entries)

	pipeline := NewPipeline([]Stage{NewLogfmtParser(false, false)}).ForStream(labels.EmptyLabels())

	// Verify that both paths agree before timing anything.
	got := pipeline.ProcessBatch(batch)
	for row := 0; row < entries; row += 97 {
		_, want, ok := pipeline.Process(0, []byte(lines[row]), labels.EmptyLabels())
		require.True(b, ok)
		require.Equal(b, want.Labels().String(), parsedLabelsRow(got, row).String(), "line %q", lines[row])
	}
	got.ParsedLabelsView.Release()

	var bytesPerOp int64
	for _, l := range lines {
		bytesPerOp += int64(len(l))
	}

	b.Run("impl=Process", func(b *testing.B) {
		b.SetBytes(bytesPerOp)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, line := range lines {
				_, _, _ = pipeline.Process(0, unsafeGetBytes(line), labels.EmptyLabels())
			}
		}
		reportPerEntry(b, entries)
	})

	b.Run("impl=ProcessBatch", func(b *testing.B) {
		b.SetBytes(bytesPerOp)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out := pipeline.ProcessBatch(batch)
			out.ParsedLabelsView.Release()
		}
		reportPerEntry(b, entries)
	})

	b.Run("impl=Scan", func(b *testing.B) {
		dec := logfmt.NewDecoder(nil)
		b.SetBytes(bytesPerOp)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, line := range lines {
				dec.Reset(unsafeGetBytes(line))
				for !dec.EOL() {
					dec.ScanKeyval()
				}
			}
		}
		reportPerEntry(b, entries)
	})
}

// generateLogfmtLines returns lines of numPairs pairs each.
func generateLogfmtLines(entries, numPairs int, long, sanitizedKeys bool, escapedPct int) []string {
	rng := rand.New(rand.NewSource(1))

	keyName := func(i int) string {
		switch {
		case sanitizedKeys && long:
			return fmt.Sprintf("service-name.label-%d", i)
		case sanitizedKeys:
			return fmt.Sprintf("a-b.%d", i)
		case long:
			return fmt.Sprintf("service_name_label_%d", i)
		default:
			return fmt.Sprintf("key_%d", i)
		}
	}

	var sb strings.Builder
	lines := make([]string, entries)
	for e := range lines {
		sb.Reset()
		for i := 0; i < numPairs; i++ {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(keyName(i))
			sb.WriteByte('=')

			escaped := rng.Intn(100) < escapedPct
			switch {
			case escaped && long:
				fmt.Fprintf(&sb, `"a long value with an \"escaped\" quote %d"`, rng.Intn(1000))
			case escaped:
				fmt.Fprintf(&sb, `"a \"%d\""`, rng.Intn(100))
			case long:
				fmt.Fprintf(&sb, "a_long_value_that_is_not_inline_%d", rng.Intn(1000))
			default:
				fmt.Fprintf(&sb, "val%d", rng.Intn(1000))
			}
		}
		lines[e] = sb.String()
	}
	return lines
}

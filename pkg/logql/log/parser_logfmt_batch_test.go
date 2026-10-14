package log

import (
	"fmt"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

// logfmtBatchLines are lines that exercise the paths of LogfmtParser.ProcessBatch
// that differ from the row by row Process: zero-copy keys and values, unescaping,
// invalid UTF-8, sanitization, errors and duplicates.
var logfmtBatchLines = []string{
	``,
	`   `,
	`level=info msg=hello`,
	`  level=info   msg=hello  `,
	// Inline and long keys and values, plain.
	`k=v`,
	`a_key_with_twelve=0123456789ab`,
	`a_rather_long_key_name_for_the_label=a_rather_long_value_that_cannot_be_inline`,
	`msg="the quick brown fox jumps over the lazy dog" level=info`,
	// Quoted without escapes: still zero-copy.
	`a="b c" d="" e=f`,
	// Escapes: the decoded value lives in the extra buffer.
	`a="x\"y" b="line\nbreak" c="a very long value with \"quotes\" inside of it"`,
	`a="éè" b="😀"`,
	// Invalid UTF-8, a literal U+FFFD and escapes that decode to it.
	"a=b\xffc d=ok",
	"a=\"b\xffc\" d=ok",
	"a=b�c",
	`a="�" b="\ud800"`,
	"a=\xff",
	"a=\"a long value with an invalid byte \xff in the middle\"",
	// Keys that need sanitization, inline and long.
	`9lives=1 a-b=2 a.b=3 a/b=4`,
	`very-long-key-name-over-twelve=1 very_long_key_name_over_twelve=2`,
	`ключ=1 k=v`,
	`a-b=1 a_b=2`, // both sanitize to a_b
	`a-b=1 a-b=2 a-b=3`,
	// Empty values and keys without a value.
	`a= b=c d`,
	`foo bar=buzz`,
	`a="" b=""`,
	// Duplicate keys.
	`a=1 a=2`,
	// Invalid escape sequences, found while unquoting into the extra buffer.
	`a="bad \x escape" b=c`,
	`a="ok" b="bad\q" c=d`,
	`a="bad \u12" b=c`,
	`a-b="bad\q" c=d`,
	// A key that sanitizes to nothing, with an invalid value. Process fails on the
	// value before it looks at the key.
	" =\"bad\\q\" c=d",
	" =\"fine\\n\" c=d",
	// Escaped values that become U+FFFD, and ones that are long and invalid.
	"a=\"x\\n\xff\" b=\"\\ud800 and a long value that is not inline\"",
	// Not logfmt.
	`foobar====wqe=sdad1r`,
	`a=1 b="unterminated`,
	`a=1 =x b=2`,
	`a=1 "b"=2 c=3`,
}

func TestLogfmtParserProcessBatch(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, keepEmpty := range []bool{false, true} {
			t.Run(fmt.Sprintf("strict=%v/keepEmpty=%v", strict, keepEmpty), func(t *testing.T) {
				p := NewLogfmtParser(strict, keepEmpty)
				b := newLogfmtBatch(logfmtBatchLines)
				b.Selection = allRows(len(logfmtBatchLines))

				got := p.ProcessBatch(b)
				for row, line := range logfmtBatchLines {
					require.Equal(t,
						processLogfmtRow(p, line).String(),
						parsedLabelsRow(got, row).String(),
						"line %q", line)
				}
			})
		}
	}
}

func TestLogfmtParserProcessBatch_Selection(t *testing.T) {
	p := NewLogfmtParser(false, false)
	lines := []string{`a=1`, `b=2`, `c=3`, `d=4`, `e=5`, `f=6`}
	b := newLogfmtBatch(lines)
	b.Selection = []int{1, 3, 4}

	got := p.ProcessBatch(b)

	offs, _, _ := got.ParsedLabelsColumns()
	require.Len(t, offs, len(lines)+1, "one entry per row, selected or not")
	for row, line := range lines {
		want := labels.EmptyLabels()
		if row == 1 || row == 3 || row == 4 {
			want = processLogfmtRow(p, line)
		}
		require.Equal(t, want.String(), parsedLabelsRow(got, row).String(), "row %d", row)
	}
	require.Equal(t, []int{1, 3, 4}, got.Selection)
}

// A sliced line column starts in the middle of its data buffer. The offsets of
// the headers must be relative to the buffer, not to the slice.
func TestLogfmtParserProcessBatch_SlicedLineColumn(t *testing.T) {
	p := NewLogfmtParser(false, false)
	lines := []string{
		`skipped_row=with_a_long_enough_value_to_move_the_offset`,
		`a_rather_long_key_name_for_the_label=a_rather_long_value_that_cannot_be_inline`,
		`short=1 s-k="e\nscaped long value that is not inline"`,
	}
	full := newLogfmtBatch(lines)

	b := &ArrowBatch{
		Timestamps: array.NewSlice(full.Timestamps, 1, 3).(*array.Int64),
		LineColumn: array.NewSlice(full.LineColumn, 1, 3).(*array.String),
		Selection:  []int{0, 1},
	}
	got := p.ProcessBatch(b)

	for row, line := range lines[1:] {
		require.Equal(t, processLogfmtRow(p, line).String(), parsedLabelsRow(got, row).String(), "line %q", line)
	}
}

// The columns come from a pool and are not zeroed. Releasing a batch and
// parsing a different one must not leak anything of the first, and two batches
// that are alive at the same time must not share memory.
func TestLogfmtParserProcessBatch_PoolReuse(t *testing.T) {
	p := NewLogfmtParser(false, false)

	check := func(t *testing.T, b *ArrowBatch, lines []string) {
		t.Helper()
		for row, line := range lines {
			require.Equal(t, processLogfmtRow(p, line).String(), parsedLabelsRow(b, row).String(), "line %q", line)
		}
	}

	big := make([]string, 0, 4*len(logfmtBatchLines))
	for i := 0; i < 4; i++ {
		big = append(big, logfmtBatchLines...)
	}
	small := []string{`a=1`, `b="x\ny"`, `a-b=3`, ``}

	for i := 0; i < 5; i++ {
		first := newLogfmtBatch(big)
		first.Selection = allRows(len(big))
		outFirst := p.ProcessBatch(first)
		check(t, outFirst, big)

		// Alive at the same time as the second batch.
		second := newLogfmtBatch(small)
		second.Selection = allRows(len(small))
		outSecond := p.ProcessBatch(second)
		check(t, outSecond, small)
		check(t, outFirst, big)

		outFirst.ParsedLabelsView.Release()
		outSecond.ParsedLabelsView.Release()

		// The memory of the first two is free now, and full of their data.
		third := newLogfmtBatch(small)
		third.Selection = allRows(len(small))
		outThird := p.ProcessBatch(third)
		check(t, outThird, small)
		outThird.ParsedLabelsView.Release()
	}
}

// The extra buffer grows through several pool buckets (4 KiB, 8 KiB, ...). What
// was written before a growth must survive it.
func TestLogfmtParserProcessBatch_LargeExtraBuffer(t *testing.T) {
	p := NewLogfmtParser(false, false)

	lines := make([]string, 3000)
	for i := range lines {
		lines[i] = fmt.Sprintf(`service-name.label-%d="a long value with an \"escaped\" quote %d" short="a \"%d\""`, i%7, i, i)
	}
	b := newLogfmtBatch(lines)
	b.Selection = allRows(len(lines))

	got := p.ProcessBatch(b)
	defer got.ParsedLabelsView.Release()

	for row, line := range lines {
		require.Equal(t, processLogfmtRow(p, line).String(), parsedLabelsRow(got, row).String(), "row %d", row)
	}
}

func TestLogfmtParserProcessBatch_NoPairs(t *testing.T) {
	p := NewLogfmtParser(false, false)
	b := newLogfmtBatch([]string{``, `   `})
	b.Selection = allRows(2)

	got := p.ProcessBatch(b)

	for row := 0; row < 2; row++ {
		require.True(t, parsedLabelsRow(got, row).IsEmpty())
	}
}

// processLogfmtRow is the oracle: the labels Process sets for line.
func processLogfmtRow(p *LogfmtParser, line string) labels.Labels {
	b := NewBaseLabelsBuilder().ForLabels(labels.EmptyLabels(), 0)
	b.Reset()
	_, _ = p.Process(0, []byte(line), b)
	return b.LabelsResult().Labels()
}

// parsedLabelsRow returns the labels of a row of ParsedLabelsView. If a key
// occurs more than once the first value wins.
func parsedLabelsRow(b *ArrowBatch, row int) labels.Labels {
	offs, keys, vals := b.ParsedLabelsColumns()
	m := map[string]string{}
	for i := int(offs[row]); i < int(offs[row+1]); i++ {
		if _, ok := m[keys.Value(i)]; !ok {
			m[keys.Value(i)] = vals.Value(i)
		}
	}
	return labels.FromMap(m)
}

func newLogfmtBatch(lines []string) *ArrowBatch {
	builder := (&ArrowBatch{}).NewBuilder()
	for i, line := range lines {
		builder.Append(int64(i), line, labels.EmptyLabels())
	}
	return builder.Result()
}

func allRows(n int) []int {
	rows := make([]int, n)
	for i := range rows {
		rows[i] = i
	}
	return rows
}

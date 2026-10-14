package logfmt

import (
	"bytes"
	"regexp"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestDecoder_Ranges(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{"plain", `a=b c=dd`},
		{"quoted", `a="b c" d=e`},
		{"empty quoted", `a="" b=c`},
		{"empty value", `a= b=c`},
		{"empty value at end", `a=b c=`},
		{"key only", `a b=c`},
		{"leading space", `   a=b`},
		{"escaped", `a="x\"y" b="é" c=d`},
		{"multibyte", `ключ=значение k="日本"`},
		{"long", `message="the quick brown fox jumps over the lazy dog" level=info`},
		{"invalid", `a=b =c d="e`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := []byte(tc.line)
			dec := NewDecoder(line)
			for dec.ScanKeyval() {
				ks, ke := dec.KeyRange()
				require.Equal(t, string(dec.Key()), string(line[ks:ke]))

				vs, ve := dec.ValueRange()
				require.LessOrEqual(t, vs, ve)
				require.LessOrEqual(t, ve, len(line))
				if dec.ValueEscaped() {
					require.NotEmpty(t, dec.Value())
					continue
				}
				require.Equal(t, string(dec.Value()), string(line[vs:ve]))
			}
		})
	}
}

func TestDecoder_RangesValues(t *testing.T) {
	line := []byte(`a="x\"y" b=cc d="" e=`)
	dec := NewDecoder(line)

	require.True(t, dec.ScanKeyval())
	vs, ve := dec.ValueRange()
	require.Equal(t, `x\"y`, string(line[vs:ve]), "raw bytes between the quotes")
	require.Equal(t, `x"y`, string(dec.Value()))
	require.True(t, dec.ValueEscaped())

	require.True(t, dec.ScanKeyval())
	vs, ve = dec.ValueRange()
	require.Equal(t, "cc", string(line[vs:ve]))
	require.False(t, dec.ValueEscaped(), "flag must reset per pair")

	require.True(t, dec.ScanKeyval())
	vs, ve = dec.ValueRange()
	require.Equal(t, vs, ve)
	require.Nil(t, dec.Value())

	require.True(t, dec.ScanKeyval())
	vs, ve = dec.ValueRange()
	require.Equal(t, vs, ve)
	require.Equal(t, len(line), vs)
}

func FuzzDecoderRanges(f *testing.F) {
	for _, s := range []string{`a=b`, `a="b\"c" d`, `=x "`, `k=v k2="v 2"`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		dec := NewDecoder(line)
		for dec.ScanKeyval() {
			ks, ke := dec.KeyRange()
			require.LessOrEqual(t, ks, ke)
			require.LessOrEqual(t, ke, len(line))
			require.Equal(t, string(dec.Key()), string(line[ks:ke]))
			vs, ve := dec.ValueRange()
			require.LessOrEqual(t, vs, ve)
			require.LessOrEqual(t, ve, len(line))
			if !dec.ValueEscaped() {
				require.Equal(t, string(dec.Value()), string(line[vs:ve]))
			}
		}
	})
}

func TestDecoder_ValueRuneError(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want bool
	}{
		{"ascii", `a=hello`, false},
		{"ascii quoted", `a="hello world"`, false},
		{"valid multibyte", `a=héllo`, false},
		{"valid multibyte quoted", `a="日本"`, false},
		{"invalid byte", "a=\xff", true},
		{"invalid byte quoted", "a=\"\xff\"", true},
		{"literal U+FFFD", "a=\ufffd", true},
		{"escaped U+FFFD", `a="\ufffd"`, true},
		{"escaped lone surrogate", `a="\ud800"`, true},
		{"escaped valid", `a="\u00e9"`, false},
		{"escaped newline", `a="x\ny"`, false},
		{"escaped then invalid", "a=\"\\n\xff\"", true},
		{"empty", `a=`, false},
		{"empty quoted", `a=""`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec := NewDecoder([]byte(tc.line))
			require.True(t, dec.ScanKeyval())
			require.Equal(t, tc.want, dec.ValueRuneError())
			require.Equal(t, bytes.ContainsRune(dec.Value(), utf8.RuneError), dec.ValueRuneError(),
				"must agree with scanning the decoded value, which is what Process does")
		})
	}
}

func TestDecoder_ValueRuneErrorResets(t *testing.T) {
	dec := NewDecoder([]byte("a=\xff b=c"))
	require.True(t, dec.ScanKeyval())
	require.True(t, dec.ValueRuneError())
	require.True(t, dec.ScanKeyval())
	require.False(t, dec.ValueRuneError(), "flag must not leak from the previous pair")
}

var plainKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func TestDecoder_KeyNeedsSanitization(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{`level=info`, false},
		{`_private=x`, false},
		{`Key_9=x`, false},
		{`k`, false}, // key without a value
		{`9lives=x`, true},
		{`a-b=x`, true},
		{`a.b=x`, true},
		{`a/b=x`, true},
		{`ключ=x`, true},
		{`k é=x`, false}, // the key is only "k"
		{`é=x`, true},
		{`a:b`, true},
	} {
		dec := NewDecoder([]byte(tc.line))
		require.True(t, dec.ScanKeyval(), tc.line)
		require.Equal(t, tc.want, dec.KeyNeedsSanitization(), tc.line)
	}

	dec := NewDecoder([]byte(`a-b=x c=y`))
	require.True(t, dec.ScanKeyval())
	require.True(t, dec.KeyNeedsSanitization())
	require.True(t, dec.ScanKeyval())
	require.False(t, dec.KeyNeedsSanitization(), "flag must not leak from the previous pair")
}

func FuzzDecoderKeyNeedsSanitization(f *testing.F) {
	for _, s := range []string{`a=b`, `9a=b`, `a-b`, "\xff=1", `é=1 k=v`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		dec := NewDecoder(line)
		for dec.ScanKeyval() {
			ks, ke := dec.KeyRange()
			require.Equal(t, !plainKeyRE.Match(line[ks:ke]), dec.KeyNeedsSanitization(), "key %q", line[ks:ke])
		}
	})
}

func TestAppendUnquoted(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"plain", `abc`, `abc`, true},
		{"empty", ``, ``, true},
		{"escaped quote", `a\"b`, `a"b`, true},
		{"escapes", `\\ \/ \' \b \f \n \r \t`, "\\ / ' \b \f \n \r \t", true},
		{"unicode", `é`, "é", true},
		{"surrogate pair", `😀`, "😀", true},
		{"lone surrogate", `\ud800`, "�", true},
		{"bad surrogate pair", `\ud800A`, "�A", true},
		{"invalid byte", "a\xffb", "a�b", true},
		{"multibyte", "日本", "日本", true},
		{"unknown escape", `\x`, ``, false},
		{"trailing backslash", `a\`, ``, false},
		{"short unicode", `\u12`, ``, false},
		{"bad hex", `\uzzzz`, ``, false},
		{"unescaped quote", `a"b`, ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := []byte("prefix")
			got, ok := appendUnquoted(prefix, []byte(tc.in))
			require.Equal(t, tc.ok, ok)
			if ok {
				require.Equal(t, "prefix"+tc.want, string(got))
			} else {
				require.Equal(t, "prefix", string(got), "dst must be unchanged on failure")
			}
		})
	}
}

// appendUnquoted must not allocate if dst has room for the worst case.
func TestAppendUnquoted_NoAllocation(t *testing.T) {
	s := []byte(`a \"quoted\" é 😀 value` + "\xff\xff")
	dst := make([]byte, 0, 3*len(s))
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = appendUnquoted(dst[:0], s)
	})
	require.Zero(t, allocs)
}

// The worst case is an invalid byte, which becomes the 3 bytes of the
// replacement rune.
func TestAppendUnquoted_Bound(t *testing.T) {
	s := bytes.Repeat([]byte{0xff}, 100)
	got, ok := appendUnquoted(nil, s)
	require.True(t, ok)
	require.Equal(t, 3*len(s), len(got))
}

func FuzzAppendUnquoted(f *testing.F) {
	for _, s := range []string{`a`, `a\"b`, `é`, `😀`, `\ud800`, "\xff", `\`, `"`, `\u12`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, s []byte) {
		want, wantOK := unquoteBytes(append(append([]byte{'"'}, s...), '"'))
		got, gotOK := appendUnquoted([]byte("prefix"), s)

		require.Equal(t, wantOK, gotOK)
		if !gotOK {
			require.Equal(t, "prefix", string(got))
			return
		}
		require.Equal(t, string(want), string(got[len("prefix"):]))
		require.LessOrEqual(t, len(got)-len("prefix"), 3*len(s))
	})
}

// scanAll scans line and returns what the caller sees for every pair, or the
// error that stops it. With deferUnquote it unquotes through AppendValue, like
// the batch parser.
func scanAll(line []byte, deferUnquote bool) []string {
	dec := NewDecoder(line)
	dec.SetDeferUnquote(deferUnquote)

	var out []string
	for !dec.EOL() {
		if !dec.ScanKeyval() {
			out = append(out, errorOf(dec))
			continue
		}
		if !deferUnquote {
			out = append(out, string(dec.Key())+"="+string(dec.Value()))
			continue
		}
		value, ok := dec.AppendValue(nil)
		if !ok {
			out = append(out, errorOf(dec))
			continue
		}
		out = append(out, string(dec.Key())+"="+string(value))
	}
	return out
}

// errorOf describes why a scan stopped. There is no error at the end of a line
// that has nothing but whitespace left.
func errorOf(dec *Decoder) string {
	if dec.Err() == nil {
		return "end"
	}
	return "error: " + dec.Err().Error()
}

func TestDecoder_DeferUnquote(t *testing.T) {
	for _, line := range []string{
		`a=b c="d e"`,
		`a="x\"y" b="é" c="😀" d=plain`,
		`a="x\ny" b="" c=`,
		"a=\"\xff\\n\" b=c",
		`a="bad \x escape" b=c`,
		`a="bad \u12" b=c`,
		`a="ok" b="bad\q" c="ok \" again"`,
		`a="unterminated \"`,
		`=x a="y\n"`,
		` `,
	} {
		t.Run(line, func(t *testing.T) {
			require.Equal(t, scanAll([]byte(line), false), scanAll([]byte(line), true))
		})
	}
}

func TestDecoder_DeferUnquoteValue(t *testing.T) {
	line := []byte(`a="x\"y" b=c`)
	dec := NewDecoder(line)
	dec.SetDeferUnquote(true)

	require.True(t, dec.ScanKeyval())
	require.True(t, dec.ValueEscaped())
	require.Nil(t, dec.Value(), "the value is not unquoted")
	vs, ve := dec.ValueRange()
	require.Equal(t, `x\"y`, string(line[vs:ve]))

	got, ok := dec.AppendValue([]byte("prefix:"))
	require.True(t, ok)
	require.Equal(t, `prefix:x"y`, string(got))

	// Values that need no unquoting are appended as they are.
	require.True(t, dec.ScanKeyval())
	require.False(t, dec.ValueEscaped())
	got, ok = dec.AppendValue(nil)
	require.True(t, ok)
	require.Equal(t, "c", string(got))
}

func FuzzDecoderDeferUnquote(f *testing.F) {
	for _, s := range []string{`a=b`, `a="x\"y" b=c`, `a="\q"`, `a="\u12"`, "a=\"\xff\\n\"", `"`, ` `} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		require.Equal(t, scanAll(line, false), scanAll(line, true))
	})
}

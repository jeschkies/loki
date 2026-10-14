// Adapted from https://github.com/go-logfmt/logfmt/ but []byte as parameter instead
// Original license is MIT.
package logfmt

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

// A Decoder reads and decodes logfmt records from an input stream.
type Decoder struct {
	pos   int
	key   []byte
	value []byte
	line  []byte
	err   error

	// Byte offsets into line of the most recently scanned pair. See KeyRange
	// and ValueRange.
	keyStart, keyEnd     int
	valueStart, valueEnd int
	valueEscaped         bool
	// valueNonASCII reports whether the raw bytes of the value contain a byte
	// >= utf8.RuneSelf. It is collected while scanning for the end of the value,
	// so it costs no extra pass.
	valueNonASCII bool
	// keyNeedsSanitization reports whether the key is not a plain label name,
	// see KeyNeedsSanitization. Like valueNonASCII it is collected during the scan.
	keyNeedsSanitization bool

	// deferUnquote makes ScanKeyval leave values with escape sequences quoted.
	// See SetDeferUnquote.
	deferUnquote bool
}

// invalidQuote is the message of the error for a quoted value with an invalid
// escape sequence.
const invalidQuote = "invalid quoted value"

// plainKeyByte[c] reports whether c may appear in a key that needs no
// sanitization: [A-Za-z0-9_].
var plainKeyByte = func() (t [256]bool) {
	for c := 'a'; c <= 'z'; c++ {
		t[c], t[c-'a'+'A'] = true, true
	}
	for c := '0'; c <= '9'; c++ {
		t[c] = true
	}
	t['_'] = true
	return t
}()

// NewDecoder returns a new decoder that reads from r.
//
// The decoder introduces its own buffering and may read data from r beyond
// the logfmt records requested.
func NewDecoder(line []byte) *Decoder {
	dec := &Decoder{line: line}
	return dec
}

func (dec *Decoder) Reset(line []byte) {
	dec.pos = 0
	dec.line = line
	dec.err = nil
}

func (dec *Decoder) EOL() bool {
	return dec.pos >= len(dec.line)
}

// ScanKeyval advances the Decoder to the next key/value pair of the current
// record, which can then be retrieved with the Key and Value methods. It
// returns false when decoding stops, either by reaching the end of the
// current record or an error.
func (dec *Decoder) ScanKeyval() bool {
	dec.key, dec.value = nil, nil
	dec.keyStart, dec.keyEnd = 0, 0
	dec.valueStart, dec.valueEnd = 0, 0
	dec.valueEscaped = false
	dec.valueNonASCII = false
	dec.keyNeedsSanitization = false

	line := dec.line

	// hi collects the OR of all value bytes. It must be declared before the
	// first goto, which cannot jump over a declaration. Only one of the value
	// loops runs per call, so it needs no reset between them.
	var hi byte

	// garbage
	for p, c := range line[dec.pos:] {
		if c > ' ' {
			dec.pos += p
			goto key
		}
	}
	dec.pos = len(line)
	return false

key:
	const invalidKeyError = "invalid key"

	start, multibyte := dec.pos, false
	unplain := false // key has a byte outside [A-Za-z0-9_]
	for p, c := range line[dec.pos:] {
		switch {
		case c == '=':
			dec.pos += p
			if dec.pos > start {
				dec.key = line[start:dec.pos]
				dec.keyStart, dec.keyEnd = start, dec.pos
				dec.keyNeedsSanitization = unplain || line[start]-'0' <= 9
				if multibyte && bytes.ContainsRune(dec.key, utf8.RuneError) {
					dec.syntaxError(invalidKeyError)
					goto skip_value
				}
			}
			if dec.key == nil {
				dec.unexpectedByte(c)
				goto skip_value
			}
			goto equal
		case c == '"':
			dec.pos += p
			dec.unexpectedByte(c)
			goto skip_value
		case c <= ' ':
			dec.pos += p
			if dec.pos > start {
				dec.key = line[start:dec.pos]
				dec.keyStart, dec.keyEnd = start, dec.pos
				dec.keyNeedsSanitization = unplain || line[start]-'0' <= 9
				if multibyte && bytes.ContainsRune(dec.key, utf8.RuneError) {
					dec.syntaxError(invalidKeyError)
					return false
				}
			}
			return true
		case c >= utf8.RuneSelf:
			multibyte = true
		}
		unplain = unplain || !plainKeyByte[c]
	}
	dec.pos = len(line)
	if dec.pos > start {
		dec.key = line[start:dec.pos]
		dec.keyStart, dec.keyEnd = start, dec.pos
		dec.keyNeedsSanitization = unplain || line[start]-'0' <= 9
		if multibyte && bytes.ContainsRune(dec.key, utf8.RuneError) {
			dec.syntaxError(invalidKeyError)
			return false
		}
	}
	return true

equal:
	dec.pos++
	dec.valueStart, dec.valueEnd = dec.pos, dec.pos
	if dec.pos >= len(line) {
		return true
	}
	switch c := line[dec.pos]; {
	case c <= ' ':
		return true
	case c == '"':
		goto qvalue
	}

	// value
	start = dec.pos
	for p, c := range line[dec.pos:] {
		switch {
		case c == '=' || c == '"':
			dec.pos += p
			dec.unexpectedByte(c)
			goto skip_value
		case c <= ' ':
			dec.pos += p
			if dec.pos > start {
				dec.value = line[start:dec.pos]
				dec.valueStart, dec.valueEnd = start, dec.pos
				dec.valueNonASCII = hi >= utf8.RuneSelf
			}
			return true
		}
		hi |= c
	}
	dec.pos = len(line)
	if dec.pos > start {
		dec.value = line[start:dec.pos]
		dec.valueStart, dec.valueEnd = start, dec.pos
		dec.valueNonASCII = hi >= utf8.RuneSelf
	}
	return true

skip_value:
	for p, c := range line[dec.pos:] {
		if c <= ' ' {
			dec.pos += p
			return false
		}
	}

	dec.pos = len(line)
	return false

qvalue:
	const untermQuote = "unterminated quoted value"

	hasEsc, esc := false, false
	start = dec.pos
	for p, c := range line[dec.pos+1:] {
		hi |= c
		switch {
		case esc:
			esc = false
		case c == '\\':
			hasEsc, esc = true, true
		case c == '"':
			dec.pos += p + 2
			dec.valueNonASCII = hi >= utf8.RuneSelf
			if hasEsc && dec.deferUnquote {
				// The caller unquotes with AppendValue, if it needs the value.
				dec.valueStart, dec.valueEnd = start+1, dec.pos-1
				dec.valueEscaped = true
				return true
			}
			if hasEsc {
				v, ok := unquoteBytes(line[start:dec.pos])
				if !ok {
					dec.syntaxError(invalidQuote)
					return false
				}
				dec.value = v
				dec.valueStart, dec.valueEnd = start+1, dec.pos-1
				dec.valueEscaped = true
			} else {
				start++
				end := dec.pos - 1
				dec.valueStart, dec.valueEnd = start, end
				if end > start {
					dec.value = line[start:end]
				}
			}
			return true
		}
	}
	dec.pos = len(line)
	dec.syntaxError(untermQuote)
	return false
}

// Key returns the most recent key found by a call to ScanKeyval. The returned
// slice may point to internal buffers and is only valid until the next call
// to ScanRecord.  It does no allocation.
func (dec *Decoder) Key() []byte {
	return dec.key
}

// Value returns the most recent value found by a call to ScanKeyval. The
// returned slice may point to internal buffers and is only valid until the
// next call to ScanRecord.  It does no allocation when the value has no
// escape sequences.
func (dec *Decoder) Value() []byte {
	return dec.value
}

// KeyRange returns the byte range [start, end) of the most recent key within
// the line passed to NewDecoder or Reset. The range is empty if the last call
// to ScanKeyval did not find a key.
func (dec *Decoder) KeyRange() (start, end int) {
	return dec.keyStart, dec.keyEnd
}

// ValueRange returns the byte range [start, end) of the most recent value
// within the line passed to NewDecoder or Reset. For a quoted value the range
// covers the content between the quotes, exactly as written, so escape
// sequences are not resolved. The range is empty if the value is empty or
// missing.
//
// If ValueEscaped reports false, line[start:end] equals Value(). Otherwise
// Value() holds the unescaped bytes in a separate buffer.
func (dec *Decoder) ValueRange() (start, end int) {
	return dec.valueStart, dec.valueEnd
}

// ValueEscaped reports whether the most recent value contained escape
// sequences, in which case Value() differs from the raw bytes of ValueRange().
func (dec *Decoder) ValueEscaped() bool {
	return dec.valueEscaped
}

// SetDeferUnquote switches the decoder to leave values with escape sequences
// quoted. ScanKeyval then reports such a value with ValueEscaped, finds its
// ValueRange, and returns true without unquoting it, so it does not allocate.
// Value() is nil for it. Callers that need the value call AppendValue, which
// unquotes into a buffer of their choice.
//
// Because the value is not unquoted, ScanKeyval does not find out if it has an
// invalid escape sequence. AppendValue does.
func (dec *Decoder) SetDeferUnquote(deferUnquote bool) {
	dec.deferUnquote = deferUnquote
}

// AppendValue appends the value found by the last call to ScanKeyval to dst. It
// is the unquoted value, as Value() would return it.
//
// If the value has an invalid escape sequence, AppendValue returns false and
// dst as it was. It sets the error that ScanKeyval would have set, so Err()
// reports it. This can only happen with SetDeferUnquote, otherwise ScanKeyval
// has already failed.
//
// An unquoted value is at most 3*len(raw) bytes long, where raw is the
// ValueRange of the value. If dst has that much spare capacity, AppendValue does
// not allocate.
func (dec *Decoder) AppendValue(dst []byte) ([]byte, bool) {
	if !dec.valueEscaped || dec.value != nil {
		// Nothing to unquote, or ScanKeyval did it.
		return append(dst, dec.value...), true
	}
	dst, ok := appendUnquoted(dst, dec.line[dec.valueStart:dec.valueEnd])
	if !ok {
		dec.syntaxError(invalidQuote)
	}
	return dst, ok
}

// ValueRuneError reports whether Value() contains utf8.RuneError, either as a
// literal U+FFFD or as an invalid UTF-8 sequence.
//
// The scan is skipped when it cannot find anything: an unescaped value that is
// pure ASCII has no byte that could decode to RuneError. An escaped value is
// always scanned, because an escape like � decodes to RuneError although
// its raw bytes are ASCII.
//
// With SetDeferUnquote an escaped value has no Value() and is not scanned, so
// ValueRuneError is false for it. Check the bytes that AppendValue returned.
func (dec *Decoder) ValueRuneError() bool {
	if !dec.valueEscaped && !dec.valueNonASCII {
		return false
	}
	return bytes.ContainsRune(dec.value, utf8.RuneError)
}

// KeyNeedsSanitization reports whether the most recent key is not usable as a
// label name as it is. A plain key matches [A-Za-z_][A-Za-z0-9_]* and is
// returned unchanged by sanitizeLabelKey in the log package, so it can be
// referenced in the line without a rewrite. Anything else has to be rewritten:
// a leading digit, a byte such as '-' or '.', or any non-ASCII byte.
//
// It is false if the last call to ScanKeyval did not find a key.
func (dec *Decoder) KeyNeedsSanitization() bool {
	return dec.keyNeedsSanitization
}

// Err returns the first non-EOF error that was encountered by the Scanner.
func (dec *Decoder) Err() error {
	return dec.err
}

func (dec *Decoder) syntaxError(msg string) {
	dec.err = &SyntaxError{
		Msg: msg,
		Pos: dec.pos + 1,
	}
}

func (dec *Decoder) unexpectedByte(c byte) {
	dec.err = &SyntaxError{
		Msg: fmt.Sprintf("unexpected %q", c),
		Pos: dec.pos + 1,
	}
}

// A SyntaxError represents a syntax error in the logfmt input stream.
type SyntaxError struct {
	Msg string
	Pos int
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("logfmt syntax error at pos %d : %s", e.Pos, e.Msg)
}

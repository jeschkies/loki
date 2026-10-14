package logfmt

import (
	"strconv"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Taken from Go's encoding/json and modified for use here.

// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

func getu4(s []byte) rune {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return -1
	}
	r, err := strconv.ParseUint(string(s[2:6]), 16, 64)
	if err != nil {
		return -1
	}
	return rune(r)
}

func unquoteBytes(s []byte) (t []byte, ok bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return
	}
	s = s[1 : len(s)-1]

	// Check for unusual characters. If there are none,
	// then no unquoting is needed, so return a slice of the
	// original bytes.
	r := 0
	for r < len(s) {
		c := s[r]
		if c == '\\' || c == '"' {
			break
		}
		if c < utf8.RuneSelf {
			r++
			continue
		}
		rr, size := utf8.DecodeRune(s[r:])
		if rr == utf8.RuneError {
			break
		}
		r += size
	}
	if r == len(s) {
		return s, true
	}

	b := make([]byte, len(s)+2*utf8.UTFMax)
	w := copy(b, s[0:r])
	for r < len(s) {
		// Out of room?  Can only happen if s is full of
		// malformed UTF-8 and we're replacing each
		// byte with RuneError.
		if w >= len(b)-2*utf8.UTFMax {
			nb := make([]byte, (len(b)+utf8.UTFMax)*2)
			copy(nb, b[0:w])
			b = nb
		}
		switch c := s[r]; {
		case c == '\\':
			r++
			if r >= len(s) {
				return
			}
			switch s[r] {
			default:
				return
			case '"', '\\', '/', '\'':
				b[w] = s[r]
				r++
				w++
			case 'b':
				b[w] = '\b'
				r++
				w++
			case 'f':
				b[w] = '\f'
				r++
				w++
			case 'n':
				b[w] = '\n'
				r++
				w++
			case 'r':
				b[w] = '\r'
				r++
				w++
			case 't':
				b[w] = '\t'
				r++
				w++
			case 'u':
				r--
				rr := getu4(s[r:])
				if rr < 0 {
					return
				}
				r += 6
				if utf16.IsSurrogate(rr) {
					rr1 := getu4(s[r:])
					if dec := utf16.DecodeRune(rr, rr1); dec != unicode.ReplacementChar {
						// A valid pair; consume.
						r += 6
						w += utf8.EncodeRune(b[w:], dec)
						break
					}
					// Invalid surrogate; fall back to replacement rune.
					rr = unicode.ReplacementChar
				}
				w += utf8.EncodeRune(b[w:], rr)
			}

		// Unescaped quote is invalid.
		case c == '"':
			return

		// ASCII
		case c < utf8.RuneSelf:
			b[w] = c
			r++
			w++

		// Coerce to well-formed UTF-8.
		default:
			rr, size := utf8.DecodeRune(s[r:])
			r += size
			w += utf8.EncodeRune(b[w:], rr)
		}
	}
	return b[0:w], true
}

// appendUnquoted appends the unquoted form of s to dst. s is the content of a
// quoted value without the quotes. It returns false, and dst as it was, if s is
// not valid.
//
// It decodes like unquoteBytes, but writes to dst instead of allocating. The
// result is at most 3*len(s) bytes longer than dst: every byte of s produces at
// most one rune, and the longest is the 3 bytes of the replacement rune for an
// invalid byte. With that much spare capacity in dst, appendUnquoted does not
// allocate.
func appendUnquoted(dst, s []byte) ([]byte, bool) {
	start := len(dst)
	for r := 0; r < len(s); {
		switch c := s[r]; {
		case c == '\\':
			r++
			if r >= len(s) {
				return dst[:start], false
			}
			switch s[r] {
			default:
				return dst[:start], false
			case '"', '\\', '/', '\'':
				dst = append(dst, s[r])
				r++
			case 'b':
				dst = append(dst, '\b')
				r++
			case 'f':
				dst = append(dst, '\f')
				r++
			case 'n':
				dst = append(dst, '\n')
				r++
			case 'r':
				dst = append(dst, '\r')
				r++
			case 't':
				dst = append(dst, '\t')
				r++
			case 'u':
				r--
				rr := getu4(s[r:])
				if rr < 0 {
					return dst[:start], false
				}
				r += 6
				if utf16.IsSurrogate(rr) {
					rr1 := getu4(s[r:])
					if dec := utf16.DecodeRune(rr, rr1); dec != unicode.ReplacementChar {
						// A valid pair; consume.
						r += 6
						dst = utf8.AppendRune(dst, dec)
						break
					}
					// Invalid surrogate; fall back to replacement rune.
					rr = unicode.ReplacementChar
				}
				dst = utf8.AppendRune(dst, rr)
			}

		// Unescaped quote is invalid.
		case c == '"':
			return dst[:start], false

		// ASCII
		case c < utf8.RuneSelf:
			dst = append(dst, c)
			r++

		// Coerce to well-formed UTF-8.
		default:
			rr, size := utf8.DecodeRune(s[r:])
			r += size
			dst = utf8.AppendRune(dst, rr)
		}
	}
	return dst, true
}

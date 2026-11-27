package exexml

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// encoding remembers how exe.xml was stored, to write it back the same way.
type encoding int

const (
	// cp1252 is Windows-1252, one byte per character, the skeleton's
	// encoding.
	cp1252 encoding = iota
	// latin1 is ISO-8859-1, one byte per character, read as the character
	// of the same value. Its bytes 0x80 to 0x9F are control characters,
	// so they are written as character references, unlike Windows-1252's.
	latin1
	// bytewise is a file declared in another encoding, e.g. Windows-1250,
	// or one that is not valid UTF-8: kept byte for byte, but only ASCII
	// is written into it, since what its other bytes mean is unknown.
	bytewise
	utf8Plain
	utf8WithBOM
)

// holds reports whether r can be written into the file as it is; others
// are written as character references.
func (e encoding) holds(r rune) bool {
	switch e {
	case cp1252:
		_, ok := to1252(r)

		return ok
	case latin1:
		return r < high1252 || r >= latin1Start && r <= maxByte
	case bytewise:
		return r < utf8.RuneSelf
	case utf8Plain, utf8WithBOM:
	}

	return true
}

// Windows-1252's bytes from high1252 to below latin1Start have characters
// of their own; the others are the character of the same value.
const (
	high1252    = 0x80
	latin1Start = 0xA0
)

// from1252 returns the characters of Windows-1252's bytes high1252 to
// latin1Start-1. The five bytes Windows-1252 leaves undefined are the
// character of the same value, so that every byte reads back as it was.
func from1252() [latin1Start - high1252]rune {
	return [...]rune{
		'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
		0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
	}
}

// to1252 returns r's Windows-1252 byte, if it has one.
func to1252(r rune) (byte, bool) {
	if r < high1252 || r >= latin1Start && r <= maxByte {
		return byte(r), true //nolint:gosec // r <= maxByte, checked above
	}

	for i, c := range from1252() {
		if c == r {
			return byte(high1252 + i), true
		}
	}

	return 0, false
}

// Byte order marks.
const (
	utf8BOM    = "\xEF\xBB\xBF"
	utf16LEBOM = "\xFF\xFE"
	utf16BEBOM = "\xFE\xFF"
)

// decode turns the file into a string. The encoding is taken from the XML
// declaration only. Files declared Windows-1252, which is MSFS's default,
// or ISO-8859-1 are read one character per byte, so writing them back
// reproduces every byte of other add-ons' entries, even ones a tool wrote
// in another encoding.
func decode(b []byte) (string, encoding, error) {
	if bytes.HasPrefix(b, []byte(utf16LEBOM)) || bytes.HasPrefix(b, []byte(utf16BEBOM)) ||
		bytes.IndexByte(b, 0) >= 0 {
		return "", cp1252, errUTF16
	}

	if bytes.HasPrefix(b, []byte(utf8BOM)) {
		return string(b[len(utf8BOM):]), utf8WithBOM, nil
	}

	declared := ""
	if m := declRE.FindSubmatch(b); m != nil {
		declared = strings.ToLower(string(m[1]))
	}

	switch declared {
	case "windows-1252", "cp1252":
		return decode1252(b), cp1252, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1":
		return bytesToRunes(b), latin1, nil
	case "", "utf-8", "utf8":
		if utf8.Valid(b) {
			return string(b), utf8Plain, nil
		}
	}

	// Declared otherwise, e.g. Windows-1250, or not valid UTF-8.
	return bytesToRunes(b), bytewise, nil
}

// decode1252 maps each byte to its Windows-1252 character, which encode
// maps back.
func decode1252(b []byte) string {
	high := from1252()
	r := make([]rune, len(b))

	for i, c := range b {
		r[i] = rune(c)
		if c >= high1252 && c < latin1Start {
			r[i] = high[c-high1252]
		}
	}

	return string(r)
}

// bytesToRunes maps each byte to the character of the same value, which
// encode maps back.
func bytesToRunes(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}

	return string(r)
}

func (e encoding) encode(doc string) ([]byte, error) {
	switch e {
	case cp1252:
		out := make([]byte, 0, len(doc))

		for _, r := range doc {
			c, ok := to1252(r)
			if !ok {
				return nil, fmt.Errorf("%w: %q", errNotEncodable, r)
			}

			out = append(out, c)
		}

		return out, nil
	case latin1, bytewise:
		out := make([]byte, 0, len(doc))

		for _, r := range doc {
			if r > maxByte {
				return nil, fmt.Errorf("%w: %q", errNotEncodable, r)
			}

			out = append(out, byte(r)) //nolint:gosec // r <= maxByte, checked above
		}

		return out, nil
	case utf8WithBOM:
		return []byte(utf8BOM + doc), nil
	case utf8Plain:
	}

	return []byte(doc), nil
}

// decodeFile decodes a file's bytes, as read describes.
func decodeFile(b []byte) (string, encoding, error) {
	doc, enc, err := decode(b)
	if err != nil {
		return "", cp1252, err
	}

	if strings.TrimSpace(doc) == "" {
		return "", cp1252, nil
	}

	return doc, enc, nil
}

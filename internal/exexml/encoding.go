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
	// latin1 is one byte per character (declared Windows-1252 or
	// ISO-8859-1), the skeleton's encoding.
	latin1 encoding = iota
	// bytewise is a file declared in another encoding, e.g. Windows-1250,
	// or one that is not valid UTF-8: kept byte for byte, but only ASCII
	// is written into it, since what its other bytes mean is unknown.
	bytewise
	utf8Plain
	utf8WithBOM
)

// maxRune is the highest character written into the file as it is.
func (e encoding) maxRune() rune {
	switch e {
	case latin1:
		return maxLatin1
	case bytewise:
		return utf8.RuneSelf - 1
	case utf8Plain, utf8WithBOM:
	}

	return utf8.MaxRune
}

// Byte order marks.
const (
	utf8BOM    = "\xEF\xBB\xBF"
	utf16LEBOM = "\xFF\xFE"
	utf16BEBOM = "\xFE\xFF"
)

// decode turns the file into a string. The encoding is taken from the XML
// declaration only. Files declared Windows-1252, which is MSFS's default,
// are read byte for byte as Latin-1, so writing them back reproduces every
// byte of other add-ons' entries, even ones a tool wrote in another
// encoding.
func decode(b []byte) (string, encoding, error) {
	if bytes.HasPrefix(b, []byte(utf16LEBOM)) || bytes.HasPrefix(b, []byte(utf16BEBOM)) ||
		bytes.IndexByte(b, 0) >= 0 {
		return "", latin1, errUTF16
	}

	if bytes.HasPrefix(b, []byte(utf8BOM)) {
		return string(b[len(utf8BOM):]), utf8WithBOM, nil
	}

	declared := ""
	if m := declRE.FindSubmatch(b); m != nil {
		declared = strings.ToLower(string(m[1]))
	}

	switch declared {
	case "windows-1252", "cp1252", "iso-8859-1", "iso8859-1", "latin1", "latin-1":
		return bytesToRunes(b), latin1, nil
	case "", "utf-8", "utf8":
		if utf8.Valid(b) {
			return string(b), utf8Plain, nil
		}
	}

	// Declared otherwise, e.g. Windows-1250, or not valid UTF-8.
	return bytesToRunes(b), bytewise, nil
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
	case latin1, bytewise:
		out := make([]byte, 0, len(doc))

		for _, r := range doc {
			if r > maxLatin1 {
				return nil, fmt.Errorf("%w: %q", errNotLatin1, r)
			}

			out = append(out, byte(r)) //nolint:gosec // r <= maxLatin1, checked above
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
		return "", latin1, err
	}

	if strings.TrimSpace(doc) == "" {
		return "", latin1, nil
	}

	return doc, enc, nil
}

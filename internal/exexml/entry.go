package exexml

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// commandLine is the entry's command line for args, each quoted as
// needed.
func commandLine(args []string) string {
	words := make([]string, 0, 1+len(args))
	words = append(words, autoArg)

	for _, a := range args {
		words = append(words, quoteArg(a))
	}

	return strings.Join(words, " ")
}

// quoteArg quotes s for a Windows command line, as CommandLineToArgvW and
// Go's flag parsing read it back.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}

	var b strings.Builder

	b.WriteByte('"')

	slashes := 0

	for _, r := range s {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}

		b.WriteRune(r)
	}

	b.WriteString(strings.Repeat(`\`, slashes)) // before the closing quote
	b.WriteByte('"')

	return b.String()
}

// entryState reports what doc says about our entry: none, one for exePath
// and commandLine and not disabled, or another. Comments inside the entry
// are ignored. Two entries of ours, e.g. one left from an older version,
// are another: Set then replaces both with one.
func entryState(doc, exePath, commandLine string) State {
	plain := withoutComments(doc)

	start, end := findEntry(doc, plain)
	if start < 0 {
		return Absent
	}

	if next, _ := findEntry(doc[end:], plain[end:]); next >= 0 {
		return Other
	}

	entry := plain[start:end]
	path, cmd := pathRE.FindStringSubmatch(entry), cmdRE.FindStringSubmatch(entry)

	if !disabledRE.MatchString(entry) && path != nil &&
		samePath(unescape(path[1]), exePath) && cmd != nil &&
		strings.TrimSpace(unescape(cmd[1])) == strings.Map(xmlRune, commandLine) {
		return Ours
	}

	return Other
}

// samePath compares Windows paths, which are case-insensitive.
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(strings.TrimSpace(a)), filepath.Clean(b))
}

// unescape resolves XML entities and character references in s, or
// returns s as it is if it isn't valid XML text.
func unescape(s string) string {
	var out string

	err := xml.Unmarshal([]byte("<p>"+s+"</p>"), &out)
	if err != nil {
		return s
	}

	return out
}

// withoutComments returns doc with every comment blanked out byte for byte,
// so that offsets into it are offsets into doc, and a commented-out entry
// is not found.
func withoutComments(doc string) string {
	b := []byte(doc)
	pos := 0

	for {
		i := strings.Index(doc[pos:], commentOpen)
		if i < 0 {
			return string(b)
		}

		i += pos

		j := strings.Index(doc[i+len(commentOpen):], commentClose)
		if j < 0 {
			j = len(doc) // unterminated: the rest is a comment
		} else {
			j += i + len(commentOpen) + len(commentClose)
		}

		for k := i; k < j; k++ {
			b[k] = ' '
		}

		pos = j
	}
}

// findEntry returns the byte range of our <Launch.Addon> block, including
// its indentation and line break, or -1, -1. Comments are skipped. Each
// closing tag pairs with the nearest opening tag before it, also one
// written with a space or attributes, so that a block never holds a second
// opening tag: an entry, ours or another add-on's, that lost its closing
// tag is not taken for part of the next one. A block counts as ours only if
// every <Name> in it is ours. plain is doc with its comments blanked, see
// withoutComments.
func findEntry(doc, plain string) (int, int) {
	pos := 0

	for {
		closing := addonCloseRE.FindStringIndex(plain[pos:])
		if closing == nil {
			return -1, -1
		}

		c, j := pos+closing[0], pos+closing[1]

		opens := addonOpenRE.FindAllStringIndex(plain[pos:c], -1)
		if len(opens) > 0 {
			i := pos + opens[len(opens)-1][0]

			if ours(plain[i:j]) {
				return lineStart(doc, i), lineEnd(doc, j)
			}
		}

		pos = j
	}
}

// ours reports whether an entry's block is ours: it has a <Name>, and every
// <Name> in it is SkyDemonConnect.
func ours(block string) bool {
	names := anyNameRE.FindAllStringSubmatch(block, -1)
	if len(names) == 0 {
		return false
	}

	for _, n := range names {
		if n[1] != entryName {
			return false
		}
	}

	return true
}

// lineStart moves i back over indentation to the start of its line.
func lineStart(doc string, i int) int {
	s := strings.LastIndex(doc[:i], "\n") + 1
	if strings.TrimSpace(doc[s:i]) != "" {
		return i
	}

	return s
}

// lineEnd moves j past a directly following line break.
func lineEnd(doc string, j int) int {
	if strings.HasPrefix(doc[j:], "\r\n") {
		return j + len("\r\n")
	}

	if strings.HasPrefix(doc[j:], "\n") {
		return j + 1
	}

	return j
}

// add returns doc with our entry for exePath and commandLine, replacing an
// existing one. Characters the file's encoding can't hold are written as
// character references.
func add(doc, exePath, commandLine string, enc encoding) (string, error) {
	doc = remove(doc)

	end := strings.LastIndex(withoutComments(doc), docClose)
	if end < 0 {
		return "", errNoDocClose
	}

	newline := "\n"
	if strings.Contains(doc, "\r\n") {
		newline = "\r\n"
	}

	block := strings.Join([]string{
		"\t" + addonOpen,
		"\t\t<Name>" + entryName + "</Name>",
		"\t\t<Disabled>False</Disabled>",
		"\t\t<Path>" + escape(exePath, enc.holds) + "</Path>",
		"\t\t<CommandLine>" + escape(commandLine, enc.holds) + "</CommandLine>",
		"\t" + addonClose,
	}, newline) + newline

	insertAt := lineStart(doc, end)
	if insertAt == end && (insertAt == 0 || doc[insertAt-1] != '\n') {
		// The closing tag shares its line with other content: insert the
		// entry inline, so that remove restores the file exactly.
		block = strings.TrimPrefix(strings.TrimSuffix(block, newline), "\t")
	}

	return doc[:insertAt] + block + doc[insertAt:], nil
}

// remove returns doc without our entry.
func remove(doc string) string {
	for {
		s, e := findEntry(doc, withoutComments(doc))
		if s < 0 {
			return doc
		}

		doc = doc[:s] + doc[e:]
	}
}

// escape escapes s for XML text: only &, < and >, which text can't hold
// as they are, so that quotes in options and apostrophes in paths stay
// literal. Runes holds rejects, which the file's encoding can't hold, are
// written as numeric character references; whether MSFS's parser resolves
// them (needed for e.g. Polish or Cyrillic user names) is not verified on
// a real install.
func escape(s string, holds func(rune) bool) string {
	var out strings.Builder

	for _, r := range s {
		r = xmlRune(r)

		switch {
		case r == '&':
			out.WriteString("&amp;")
		case r == '<':
			out.WriteString("&lt;")
		case r == '>':
			out.WriteString("&gt;")
		case !holds(r):
			_, _ = fmt.Fprintf(&out, "&#x%X;", r)
		default:
			out.WriteRune(r)
		}
	}

	return out.String()
}

// xmlRune replaces a rune XML 1.0 doesn't allow in text with U+FFFD.
func xmlRune(r rune) rune {
	if r < ' ' && r != '\t' && r != '\n' && r != '\r' || r == 0xFFFE || r == 0xFFFF {
		return utf8.RuneError
	}

	return r
}

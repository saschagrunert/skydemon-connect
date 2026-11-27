// Package exexml adds or removes SkyDemonConnect in MSFS 2024's exe.xml, the
// list of programs the simulator starts with itself.
//
// The file is edited as text rather than re-encoded through encoding/xml,
// so that every other add-on's entry, comment and formatting is kept byte
// for byte.
package exexml

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// entryName is the <Name> of our entry.
	entryName = "SkyDemonConnect"
	// autoArg starts the entry's command line: start quietly, exit with
	// MSFS.
	autoArg = "-auto"

	addonOpen    = "<Launch.Addon>"
	addonClose   = "</Launch.Addon>"
	commentOpen  = "<!--"
	commentClose = "-->"
	docClose     = "</SimBase.Document>"
	backupExt    = ".bak-skydemonconnect"
	fileMode     = 0o644
	maxLatin1    = 0xFF
	// project is where the README shows the entry to add by hand.
	project = "github.com/saschagrunert/skydemon-connect"
	// byHand is what to do about a file this package won't change.
	byHand = "; add the entry by hand, see " + project
	// replaceTries and replaceWait bound the retries of replacing a file
	// that another program, e.g. a virus scanner or MSFS, has open.
	replaceTries = 10
	replaceWait  = 100 * time.Millisecond
)

// skeleton is what a new exe.xml starts as.
const skeleton = `<?xml version="1.0" encoding="Windows-1252"?>
<SimBase.Document Type="SimConnect" version="1,0">
	<Descr>Auto launch external applications on MSFS start</Descr>
	<Filename>exe.xml</Filename>
	<Disabled>False</Disabled>
	<Launch.ManualLoad>False</Launch.ManualLoad>
</SimBase.Document>
`

var (
	// nameRE finds our entry, also when hand-edited with extra spaces.
	nameRE = regexp.MustCompile(`<Name>\s*` + entryName + `\s*</Name>`)
	// disabledRE finds an entry switched off in MSFS's own way.
	disabledRE = regexp.MustCompile(`(?i)<Disabled>\s*True\s*</Disabled>`)
	// pathRE finds the entry's <Path>, cmdRE its <CommandLine>.
	pathRE = regexp.MustCompile(`(?s)<Path>(.*?)</Path>`)
	cmdRE  = regexp.MustCompile(`(?s)<CommandLine>(.*?)</CommandLine>`)
	// declRE finds the encoding of the XML declaration, which can only be
	// at the very start of the file. [^>] keeps the match inside it.
	declRE = regexp.MustCompile(`\A\s*<\?xml\b[^>]*?\bencoding\s*=\s*["']([^"']*)["']`)

	errNoInstall = errors.New("MSFS 2024 not found (Microsoft Store or Steam). " +
		"Start MSFS once, or add the entry by hand, see " + project)
	errChanged    = errors.New("changed while it was being edited, try again")
	errNoDocClose = errors.New("exe.xml has no " + docClose + ", not touching it" + byHand)
	errUTF16      = errors.New("exe.xml is UTF-16 or binary, not touching it" + byHand)
	errNotLatin1  = errors.New("character can't be written to a Windows-1252 exe.xml")
	errNotFile    = errors.New("is not a file")
)

// Installs are the exe.xml files of the MSFS 2024 installations on a PC.
// The files need not exist yet.
type Installs []string

// Find returns the Installs of this PC: Microsoft Store / Xbox app, then
// Steam, as far as their user folders exist.
func Find() Installs { return find(os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")) }

// find returns the Installs below the given profile folders.
func find(localAppData, appData string) Installs {
	var out Installs

	for _, dir := range []string{
		filepath.Join(localAppData, "Packages", "Microsoft.Limitless_8wekyb3d8bbwe", "LocalCache"),
		filepath.Join(appData, "Microsoft Flight Simulator 2024"),
	} {
		fi, err := os.Stat(dir)
		if err == nil && fi.IsDir() && filepath.IsAbs(dir) {
			out = append(out, filepath.Join(dir, "exe.xml"))
		}
	}

	return out
}

// State is what the exe.xml files say about SkyDemonConnect.
type State int

const (
	// Absent means no file has an entry for SkyDemonConnect.
	Absent State = iota
	// Other means an entry exists, but not for this exe and these options,
	// e.g. from before the exe was moved, from another copy, or disabled.
	// Set(true) replaces it, Set(false) removes it.
	Other
	// Ours means an entry launches SkyDemonConnect from this exe with
	// these options.
	Ours
)

// State reports whether any of the files launches SkyDemonConnect from
// exePath with args, as Set writes them (Ours), has an entry for it that
// differs (Other), or none (Absent). An entry marked
// <Disabled>True</Disabled> counts as Other.
func (in Installs) State(exePath string, args []string) State {
	state := Absent

	for _, p := range in {
		doc, _, err := read(p)
		if err != nil {
			continue
		}

		switch entryState(doc, exePath, commandLine(args)) {
		case Ours:
			return Ours
		case Other:
			state = Other
		case Absent:
		}
	}

	return state
}

// Set adds (enable) or removes our entry in every file. The entry's
// command line is -auto followed by args, each quoted for the Windows
// command line as needed, e.g. "-name=My Sim". Enabling replaces an
// existing entry, also one that was disabled by hand or has other args.
// Before the first change of an existing file, it is copied to
// exe.xml.bak-skydemonconnect; if that fails, the file is left alone. A
// file that can't be changed doesn't stop the others.
func (in Installs) Set(enable bool, exePath string, args []string) error {
	if len(in) == 0 {
		return errNoInstall
	}

	var errs []error

	commandLine := commandLine(args)

	for _, p := range in {
		err := update(p, enable, exePath, commandLine)
		if err != nil {
			errs = append(errs, inFile(p, err))
		}
	}

	return errors.Join(errs...)
}

// inFile names path in err, unless an error from the os package inside it
// already does.
func inFile(path string, err error) error {
	var (
		pathErr *fs.PathError
		linkErr *os.LinkError
	)

	if errors.As(err, &pathErr) || errors.As(err, &linkErr) {
		return err
	}

	return fmt.Errorf("%s: %w", path, err)
}

func update(path string, enable bool, exePath, commandLine string) error {
	// A symlinked exe.xml stays a symlink: the file it points to is
	// edited.
	path = resolve(path)

	orig, err := readRaw(path)
	if err != nil {
		return err
	}

	doc, enc, err := decodeFile(orig)
	if err != nil {
		return err
	}

	existed := doc != ""
	if !existed && !enable {
		return nil // nothing to remove, and no reason to create the file
	}

	out, err := edited(doc, enable, exePath, commandLine, enc)
	if err != nil {
		return err
	}

	if out == doc {
		return nil
	}

	b, err := enc.encode(out)
	if err != nil {
		return err
	}

	if existed {
		err = backup(path)
		if err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}

	return writeIfUnchanged(path, orig, b)
}

// writeIfUnchanged replaces path with b, unless it no longer holds orig:
// another installer changed it meanwhile, whose change mustn't be undone.
func writeIfUnchanged(path string, orig, b []byte) error {
	now, err := readRaw(path)
	if err != nil {
		return err
	}

	if !bytes.Equal(now, orig) {
		return errChanged
	}

	return writeFile(path, b)
}

// resolve follows symlinks to the file path names; a path that doesn't
// resolve, e.g. a missing file, stays as it is.
func resolve(path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}

	return target
}

// edited returns doc with our entry added or removed; an empty doc starts
// as the skeleton.
func edited(doc string, enable bool, exePath, commandLine string, enc encoding) (string, error) {
	if doc == "" {
		doc = skeleton
	}

	if !enable {
		return remove(doc), nil
	}

	return add(doc, exePath, commandLine, enc)
}

// backup copies path once; later changes keep the original backup.
func backup(path string) error {
	bak := path + backupExt

	fi, err := os.Stat(bak)
	if err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s %w", filepath.Base(bak), errNotFile)
		}

		return nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	orig, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return writeFile(bak, orig)
}

// writeFile replaces path with b atomically: a crash or a full disk leaves
// either the old file or the new one, never a truncated one.
func writeFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	tmp := f.Name()

	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}

	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Chmod(tmp, fileMode)
	}

	if err == nil {
		err = replace(tmp, path, os.Rename, busy, time.Sleep)
	}

	if err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("write: %w", err)
	}

	return nil
}

// replace renames tmp to path, which replaces path, also on Windows. A
// file another program has open, e.g. a virus scanner just checking the
// new one, can't be replaced for a moment: busy errors are retried for
// about a second.
func replace(
	tmp, path string,
	rename func(string, string) error,
	busy func(error) bool,
	sleep func(time.Duration),
) error {
	var err error

	for try := range replaceTries {
		if try > 0 {
			sleep(replaceWait)
		}

		err = rename(tmp, path)
		if err == nil || !busy(err) {
			return err
		}
	}

	return err
}

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
// are ignored.
func entryState(doc, exePath, commandLine string) State {
	plain := withoutComments(doc)

	s, e := findEntry(doc, plain)
	if s < 0 {
		return Absent
	}

	entry := plain[s:e]
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
// closing tag pairs with the nearest opening tag before it, so that
// another add-on's entry with a missing closing tag is not taken for part
// of ours. plain is doc with its comments blanked, see withoutComments.
func findEntry(doc, plain string) (int, int) {
	pos := 0

	for {
		c := strings.Index(plain[pos:], addonClose)
		if c < 0 {
			return -1, -1
		}

		c += pos
		j := c + len(addonClose)

		i := strings.LastIndex(plain[pos:c], addonOpen)
		if i >= 0 {
			i += pos

			if nameRE.MatchString(plain[i:j]) {
				return lineStart(doc, i), lineEnd(doc, j)
			}
		}

		pos = j
	}
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
		"\t\t<Path>" + escape(exePath, enc.maxRune()) + "</Path>",
		"\t\t<CommandLine>" + escape(commandLine, enc.maxRune()) + "</CommandLine>",
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
// literal. Runes above maxRune, which the file's encoding can't hold, are
// written as numeric character references; whether MSFS's parser resolves
// them (needed for e.g. Polish or Cyrillic user names) is not verified on
// a real install.
func escape(s string, maxRune rune) string {
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
		case r > maxRune:
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

// read returns the file's text and encoding. A missing or blank file reads
// as empty, in the skeleton's encoding.
func read(path string) (string, encoding, error) {
	b, err := readRaw(path)
	if err != nil {
		return "", latin1, err
	}

	return decodeFile(b)
}

// readRaw returns the file's bytes; a missing file has none.
func readRaw(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	return b, err
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

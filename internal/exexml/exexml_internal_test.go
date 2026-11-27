package exexml

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const sample = "<?xml version=\"1.0\" encoding=\"Windows-1252\"?>\r\n" +
	"<SimBase.Document Type=\"SimConnect\" version=\"1,0\">\r\n" +
	"\t<Descr>Launch</Descr>\r\n" +
	"\t<Filename>EXE.xml</Filename>\r\n" +
	"\t<Disabled>False</Disabled>\r\n" +
	"\t<Launch.ManualLoad>False</Launch.ManualLoad>\r\n" +
	"\t<Launch.Addon>\r\n" +
	"\t\t<Name>SPAD.neXT</Name>\r\n" +
	"\t\t<Disabled>False</Disabled>\r\n" +
	"\t\t<Path>c:\\Program Files\\SPAD.neXt\\SPAD.neXt.exe</Path>\r\n" +
	"\t\t<CommandLine>-m</CommandLine>\r\n" +
	"\t</Launch.Addon>\r\n" +
	"</SimBase.Document>\r\n"

// enabled reports whether installs launch exe with args.
func enabled(installs Installs, exe string, args []string) bool {
	return installs.State(exe, args) == Ours
}

// has reports whether doc contains our entry for exePath and commandLine.
func has(doc, exePath, commandLine string) bool {
	return entryState(doc, exePath, commandLine) == Ours
}

func wellFormed(t *testing.T, doc string) {
	t.Helper()

	d := xml.NewDecoder(strings.NewReader(doc))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }

	for {
		_, err := d.Token()
		if errors.Is(err, io.EOF) {
			return
		}

		if err != nil {
			t.Fatalf("not well-formed: %v\n%s", err, doc)
		}
	}
}

func TestAddRemove(t *testing.T) {
	t.Parallel()

	path := `C:\Users\sascha\Sim Tools & Co\SkyDemonConnect.exe`

	out, err := add(sample, path, autoArg, cp1252)
	if err != nil {
		t.Fatal(err)
	}

	if !has(out, path, autoArg) || !strings.Contains(out, "SPAD.neXT") {
		t.Fatalf("entry missing or other entry lost:\n%s", out)
	}

	if !strings.Contains(out, "Sim Tools &amp; Co") ||
		!strings.Contains(out, "<CommandLine>-auto</CommandLine>") {
		t.Errorf("path not escaped or args missing:\n%s", out)
	}

	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Error("mixed line endings")
	}

	wellFormed(t, out)

	again, err := add(out, path, autoArg, cp1252)
	if err != nil || strings.Count(again, "<Name>SkyDemonConnect</Name>") != 1 {
		t.Errorf("adding twice should keep one entry (err %v)", err)
	}

	if back := remove(out); back != sample {
		t.Errorf("remove did not restore the original:\n%q\n%q", back, sample)
	}
}

func TestAddLayouts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, doc string }{
		{"skeleton", skeleton},
		{"one line", `<SimBase.Document><Launch.Addon><Name>A</Name></Launch.Addon></SimBase.Document>`},
		{"no final newline", "<SimBase.Document>\n\t<Launch.Addon><Name>A</Name></Launch.Addon>\n" +
			"</SimBase.Document>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := add(tc.doc, `C:\x.exe`, autoArg, cp1252)
			if err != nil {
				t.Fatal(err)
			}

			wellFormed(t, out)

			if !has(out, `C:\x.exe`, autoArg) {
				t.Error("no entry")
			}

			if back := remove(out); back != tc.doc {
				t.Errorf("remove did not restore:\n%q\n%q", back, tc.doc)
			}
		})
	}

	_, err := add("<foo/>", `C:\x.exe`, autoArg, cp1252)
	if err == nil {
		t.Error("a file without </SimBase.Document> must be refused")
	}
}

func TestHandEditedName(t *testing.T) {
	t.Parallel()

	doc := strings.Replace(sample, "<Name>SPAD.neXT</Name>", "<Name> SkyDemonConnect </Name>", 1)
	if !has(doc, `C:\Program Files\SPAD.neXt\SPAD.neXt.exe`, "-m") {
		t.Error("entry with spaces, or path in another case, not found")
	}

	if has(sample, `c:\Program Files\SPAD.neXt\SPAD.neXt.exe`, autoArg) {
		t.Error("found an entry that isn't there")
	}
}

// setup makes the Store and Steam folders under tmp's Local and Roaming
// and returns them as Installs, with the two exe.xml paths.
func setup(t *testing.T) (Installs, string, string) {
	t.Helper()

	return setupIn(t, t.TempDir())
}

func setupIn(t *testing.T, tmp string) (Installs, string, string) {
	t.Helper()

	storeDir := filepath.Join(
		tmp,
		"Local",
		"Packages",
		"Microsoft.Limitless_8wekyb3d8bbwe",
		"LocalCache",
	)
	steamDir := filepath.Join(tmp, "Roaming", "Microsoft Flight Simulator 2024")

	for _, d := range []string{storeDir, steamDir} {
		err := os.MkdirAll(d, 0o750)
		if err != nil {
			t.Fatal(err)
		}
	}

	installs := find(filepath.Join(tmp, "Local"), filepath.Join(tmp, "Roaming"))

	return installs, filepath.Join(storeDir, "exe.xml"), filepath.Join(steamDir, "exe.xml")
}

// asUTF8 reads a file that isn't UTF-8 as Latin-1, for the XML parser.
func asUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}

	r := make([]rune, len(s))
	for i := range len(s) {
		r[i] = rune(s[i])
	}

	return string(r)
}

func readFile(t *testing.T, p string) string {
	t.Helper()

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func TestSet(t *testing.T) {
	t.Parallel()

	installs, store, steam := setup(t)

	err := os.WriteFile(store, []byte(sample), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	const exe = `C:\Tools\Jürgen\SkyDemonConnect.exe`

	if len(installs) != 2 || enabled(installs, exe, nil) {
		t.Fatalf("installs %v, enabled %v", installs, enabled(installs, exe, nil))
	}

	err = installs.Set(true, exe, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !enabled(installs, exe, nil) || !enabled(installs, strings.ToUpper(exe), nil) {
		t.Fatal("not enabled")
	}

	if got := readFile(t, store+".bak-skydemonconnect"); got != sample {
		t.Error("backup differs from the original")
	}

	if got := readFile(t, store); !strings.Contains(got, "J\xfcrgen") {
		t.Error("ü not written as Windows-1252")
	}

	if got := readFile(t, steam); !strings.Contains(got, "<Name>SkyDemonConnect</Name>") {
		t.Error("Steam exe.xml not created")
	}

	// After moving the exe, the old entry doesn't count, and enabling
	// replaces it, without overwriting the backup of the original.
	const moved = `C:\Other\SkyDemonConnect.exe`
	if enabled(installs, moved, nil) {
		t.Error("an entry for the old path counts for the moved exe")
	}

	err = installs.Set(true, moved, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !enabled(installs, moved, nil) || enabled(installs, exe, nil) ||
		strings.Count(readFile(t, store), "<Name>"+entryName+"</Name>") != 1 {
		t.Errorf("entry not replaced:\n%s", readFile(t, store))
	}

	if got := readFile(t, store+".bak-skydemonconnect"); got != sample {
		t.Error("backup was overwritten")
	}

	err = installs.Set(false, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if enabled(installs, moved, nil) || readFile(t, store) != sample {
		t.Error("disable did not restore the original")
	}
}

func TestSetDisableWithoutFiles(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)

	err := installs.Set(false, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = os.Stat(store)
	if !errors.Is(err, os.ErrNotExist) {
		t.Error("disabling must not create exe.xml")
	}
}

func TestSetNoInstall(t *testing.T) {
	t.Parallel()

	installs := find(t.TempDir(), t.TempDir())
	if len(installs) != 0 {
		t.Fatalf("installs without MSFS folders: %v", installs)
	}

	err := installs.Set(true, `C:\x.exe`, nil)
	if !errors.Is(err, errNoInstall) {
		t.Errorf("expected errNoInstall without MSFS folders: %v", err)
	}

	if enabled(installs, `C:\x.exe`, nil) {
		t.Error("enabled without MSFS folders")
	}
}

func TestFind(t *testing.T) {
	tmp := t.TempDir()
	_, store, steam := setupIn(t, tmp)

	t.Setenv("LOCALAPPDATA", filepath.Join(tmp, "Local"))
	t.Setenv("APPDATA", filepath.Join(tmp, "Roaming"))

	if got := Find(); len(got) != 2 || got[0] != store || got[1] != steam {
		t.Errorf("Find() = %v, want [%s %s]", got, store, steam)
	}

	t.Setenv("LOCALAPPDATA", "relative")

	if got := Find(); len(got) != 1 || got[0] != steam {
		t.Errorf("a relative folder must be skipped: %v", got)
	}
}

// TestSetWrites checks what Set writes into files of each kind.
func TestSetWrites(t *testing.T) {
	t.Parallel()

	withEntry, err := add(sample, `C:\x.exe`, autoArg+" -com COM1", cp1252)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, doc, exe string
		args           []string
		want           []string
	}{
		{
			// A character XML doesn't allow would make the file
			// malformed: it is replaced.
			name: "control character",
			doc:  sample,
			exe:  `C:\x.exe`,
			args: []string{"-name=a\x01b"},
			want: []string{"<CommandLine>-auto -name=a&#xFFFD;b</CommandLine>"},
		},
		{
			// Characters outside Windows-1252 become references; its own
			// at 0x80 to 0x9F, e.g. € and Š, are its bytes.
			name: "references",
			doc:  sample,
			exe:  `C:\Users\Łukasz €\Šárka\SkyDemonConnect.exe`,
			want: []string{"<Path>C:\\Users\\&#x141;ukasz \x80\\\x8a\xe1rka\\SkyDemonConnect.exe</Path>"},
		},
		{
			name: "UTF-8 keeps characters",
			doc:  strings.Replace(sample, "Windows-1252", "utf-8", 1),
			exe:  `C:\Łukasz\SkyDemonConnect.exe`,
			want: []string{`<Path>C:\Łukasz\SkyDemonConnect.exe</Path>`},
		},
		{
			// A blank file starts as the Windows-1252 skeleton.
			name: "blank file",
			doc:  " \r\n",
			exe:  `C:\Users\Jörg\SkyDemonConnect.exe`,
			want: []string{`<?xml version="1.0" encoding="Windows-1252"?>`, "J\xf6rg"},
		},
		{
			// Declared Windows-1250, with a byte that is not UTF-8: what
			// bytes above 0x7F mean is unknown, so only ASCII is written,
			// and the other bytes stay.
			name: "undeclared bytes",
			doc: strings.Replace(
				strings.Replace(sample, "Windows-1252", "Windows-1250", 1),
				"SPAD.neXT</Name>", "SPAD\xe8</Name>", 1),
			exe:  `C:\Users\Jürgen\SkyDemonConnect.exe`,
			want: []string{`<Path>C:\Users\J&#xFC;rgen\SkyDemonConnect.exe</Path>`, "SPAD\xe8</Name>"},
		},
		{
			// Quotes and apostrophes stay literal: whether MSFS resolves
			// character references is unknown. Only &, < and > are
			// escaped.
			name: "apostrophe and ampersand",
			doc:  sample,
			exe:  `C:\Users\O'Brien & Co\SkyDemonConnect.exe`,
			want: []string{`<Path>C:\Users\O'Brien &amp; Co\SkyDemonConnect.exe</Path>`},
		},
		{
			// Options are passed on, quoted for the command line where
			// they need it, the quotes as they are.
			name: "options",
			doc:  sample,
			exe:  `C:\x.exe`,
			args: []string{"-udp=auto,192.168.1.23:49002", "-name=My Sim"},
			want: []string{
				`<CommandLine>-auto -udp=auto,192.168.1.23:49002 "-name=My Sim"</CommandLine>`,
			},
		},
		{
			// < and > in an option are escaped, and read back.
			name: "angle brackets",
			doc:  sample,
			exe:  `C:\x.exe`,
			args: []string{"-name=<a>"},
			want: []string{"<CommandLine>-auto -name=&lt;a&gt;</CommandLine>"},
		},
		{
			// An entry with other options is replaced with these.
			name: "new options",
			doc:  withEntry,
			exe:  `C:\x.exe`,
			want: []string{"<CommandLine>-auto</CommandLine>"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			installs, store, _ := setup(t)

			err := os.WriteFile(store, []byte(tc.doc), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			err = installs.Set(true, tc.exe, tc.args)
			if err != nil {
				t.Fatal(err)
			}

			got := readFile(t, store)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("no %q in:\n%s", w, got)
				}
			}

			if n := strings.Count(got, "<Name>"+entryName+"</Name>"); n != 1 {
				t.Errorf("%d entries", n)
			}

			wellFormed(t, asUTF8(got))

			if !enabled(installs, tc.exe, tc.args) {
				t.Error("the entry doesn't match its path and options")
			}

			if len(tc.args) > 0 && installs.State(tc.exe, nil) != Other {
				t.Error("an entry with other options isn't another entry")
			}
		})
	}
}

func TestDisabledEntry(t *testing.T) {
	t.Parallel()

	out, err := add(sample, `C:\x.exe`, autoArg, cp1252)
	if err != nil {
		t.Fatal(err)
	}

	off := strings.Replace(out, "<Name>SkyDemonConnect</Name>\r\n\t\t<Disabled>False",
		"<Name>SkyDemonConnect</Name>\r\n\t\t<Disabled> true", 1)
	if off == out {
		t.Fatal("test setup: entry not switched off")
	}

	if has(off, `C:\x.exe`, autoArg) {
		t.Error("a disabled entry counts as enabled")
	}

	on, err := add(off, `C:\x.exe`, autoArg, cp1252)
	if err != nil || !has(on, `C:\x.exe`, autoArg) || strings.Count(on, entryName) != 1 {
		t.Errorf("enabling must replace the disabled entry (err %v):\n%s", err, on)
	}

	if back := remove(off); back != sample {
		t.Errorf("remove must also drop a disabled entry:\n%q", back)
	}
}

func TestCommentedEntry(t *testing.T) {
	t.Parallel()

	comment := "\t<!--\r\n\t<Launch.Addon>\r\n\t\t<Name>SkyDemonConnect</Name>\r\n" +
		"\t</Launch.Addon>\r\n\t</SimBase.Document>\r\n\t-->\r\n"
	doc := strings.Replace(sample, "</SimBase.Document>", comment+"</SimBase.Document>", 1)

	if has(doc, `C:\x.exe`, autoArg) {
		t.Error("a commented-out entry counts as enabled")
	}

	if remove(doc) != doc {
		t.Error("remove changed a comment")
	}

	out, err := add(doc, `C:\x.exe`, autoArg, cp1252)
	if err != nil {
		t.Fatal(err)
	}

	if !has(out, `C:\x.exe`, autoArg) || !strings.Contains(out, comment) {
		t.Errorf("entry not added after the comment:\n%s", out)
	}

	if !strings.HasSuffix(out, "\t</Launch.Addon>\r\n</SimBase.Document>\r\n") {
		t.Errorf("entry not added before the real closing tag:\n%s", out)
	}

	if remove(out) != doc {
		t.Error("remove did not restore the original")
	}

	if has("<SimBase.Document><!-- <Launch.Addon><Name>SkyDemonConnect</Name>"+
		"<Path>C:\\x.exe</Path></Launch.Addon>", `C:\x.exe`, autoArg) {
		t.Error("an unterminated comment is not skipped")
	}
}

func TestSetRefusesUTF16(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)

	doc := make([]byte, 0, 2+2*len(sample))
	doc = append(doc, 0xFF, 0xFE)

	for _, c := range sample {
		doc = append(doc, byte(c), 0)
	}

	err := os.WriteFile(store, doc, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if !errors.Is(err, errUTF16) {
		t.Errorf("UTF-16 exe.xml accepted: %v", err)
	}

	if !bytes.Equal([]byte(readFile(t, store)), doc) {
		t.Error("a refused file must be left alone")
	}
}

func TestSetReadError(t *testing.T) {
	t.Parallel()

	installs, store, steam := setup(t)

	err := os.Mkdir(store, 0o750) // reading a directory fails
	if err != nil {
		t.Fatal(err)
	}

	// The error names the file as resolved: on Windows, t.TempDir can be
	// an 8.3 short path, which resolving makes long.
	path, err := filepath.EvalSymlinks(store)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if err == nil || strings.Count(err.Error(), path) != 1 ||
		!strings.HasPrefix(err.Error(), "read "+path+": ") {
		t.Errorf("read error not reported once, with its path: %v", err)
	}

	if got := readFile(t, steam); !strings.Contains(got, entryName) {
		t.Error("one broken exe.xml must not stop the others")
	}
}

func TestSetLeavesNoTempFiles(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)

	err := os.WriteFile(store, []byte(sample), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Dir(store))
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	if len(names) != 3 { // exe.xml, its first and its latest backup
		t.Errorf("files left behind: %v", names)
	}
}

func TestWriteFileCleansUp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "exe.xml")

	err := os.Mkdir(target, 0o750) // the rename onto a directory fails
	if err != nil {
		t.Fatal(err)
	}

	err = writeFile(target, []byte("x"))
	if err == nil {
		t.Fatal("writing over a directory succeeded")
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("temp file left behind: %v %v", entries, err)
	}
}

func TestSetBackupFailureAborts(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)

	err := os.WriteFile(store, []byte(sample), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	// Something that is not a file where the backup goes.
	err = os.Mkdir(store+backupExt, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if err == nil || !strings.Contains(err.Error(), "backup") {
		t.Errorf("failed backup not reported: %v", err)
	}

	if readFile(t, store) != sample {
		t.Error("the file was changed without a backup")
	}
}

func TestMalformedOtherEntry(t *testing.T) {
	t.Parallel()

	// Another add-on's entry lacks its closing tag: ours must still be
	// found on its own, and removing it must keep the other's text.
	broken := strings.Replace(sample, "\t</Launch.Addon>\r\n", "", 1)

	out, err := add(broken, `C:\x.exe`, autoArg, cp1252)
	if err != nil {
		t.Fatal(err)
	}

	if !has(out, `C:\x.exe`, autoArg) {
		t.Fatalf("entry not found:\n%s", out)
	}

	if back := remove(out); back != broken {
		t.Errorf("remove changed the other entry:\n%q\n%q", back, broken)
	}
}

func TestDisabledInComment(t *testing.T) {
	t.Parallel()

	out, err := add(sample, `C:\x.exe`, autoArg, cp1252)
	if err != nil {
		t.Fatal(err)
	}

	doc := strings.Replace(out, "<Disabled>False</Disabled>\r\n\t\t<Path>C:",
		"<!-- was <Disabled>True</Disabled> --><Disabled>False</Disabled>\r\n\t\t<Path>C:", 1)
	if doc == out {
		t.Fatal("test setup: comment not inserted")
	}

	if !has(doc, `C:\x.exe`, autoArg) {
		t.Error("a commented-out <Disabled>True</Disabled> counts")
	}
}

func TestEntityInPath(t *testing.T) {
	t.Parallel()

	const exe = `C:\Sim Tools & Co\SkyDemonConnect.exe`

	out, err := add(sample, exe, autoArg, cp1252)
	if err != nil {
		t.Fatal(err)
	}

	if !has(out, exe, autoArg) || has(out, `C:\Sim Tools &amp; Co\SkyDemonConnect.exe`, autoArg) {
		t.Error("the path is not compared after unescaping")
	}
}

func TestSetPermissionError(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file the user can't read")
	}

	installs, store, _ := setup(t)

	err := os.WriteFile(store, []byte(sample), 0)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if err == nil || strings.Count(err.Error(), store) != 1 ||
		!strings.HasPrefix(err.Error(), "open "+store+": ") {
		t.Errorf("permission error not reported once, with its path: %v", err)
	}
}

func TestSetOwnErrorNamesFile(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)

	err := os.WriteFile(store, []byte("<foo/>"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if !errors.Is(err, errNoDocClose) || !strings.HasPrefix(err.Error(), store+": ") {
		t.Errorf("own error without the file's path: %v", err)
	}
}

// State tells our entry from another one, e.g. for a moved exe, other
// options, or one disabled by hand, and from none.
func TestState(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)
	exe, args := `C:\x.exe`, []string{"-com=COM11"}

	if got := installs.State(exe, args); got != Absent {
		t.Errorf("no files: %v", got)
	}

	err := installs.Set(true, exe, args)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		exe  string
		args []string
		want State
	}{
		{exe, args, Ours},
		{`C:\Moved\x.exe`, args, Other},
		{exe, nil, Other},
	} {
		if got := installs.State(tc.exe, tc.args); got != tc.want {
			t.Errorf("State(%s, %q) = %v, want %v", tc.exe, tc.args, got, tc.want)
		}
	}

	// Disable our entry by hand, as MSFS's own switch does.
	doc := readFile(t, store)
	i := strings.Index(doc, "<Name>"+entryName+"</Name>")
	disabled := doc[:i] + strings.Replace(doc[i:], "<Disabled>False", "<Disabled>True", 1)

	err = os.WriteFile(store, []byte(disabled), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	// The Steam file still has the enabled entry: look at this one only.
	if got := (Installs{store}).State(exe, args); got != Other {
		t.Errorf("a disabled entry: %v, want Other", got)
	}
}

func TestQuoteArg(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ arg, want string }{
		{`-udp=auto`, `-udp=auto`},
		{``, `""`},
		{`-name=a b`, `"-name=a b"`},
		{`-name=a"b`, `"-name=a\"b"`},
		{`-name=a\ b\`, `"-name=a\ b\\"`},
	} {
		if got := quoteArg(tc.arg); got != tc.want {
			t.Errorf("quoteArg(%q) = %q, want %q", tc.arg, got, tc.want)
		}
	}
}

var errBusy = errors.New("busy")

// Replacing a file another program has open is retried for a while.
func TestReplaceRetries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		fails int
		err   error
		calls int
	}{
		{"works", 0, nil, 1},
		{"busy for a moment", 3, nil, 4},
		{"busy for too long", replaceTries + 5, errBusy, replaceTries},
		{"another error", 1, errNotFile, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			calls, slept := 0, time.Duration(0)
			rename := func(string, string) error {
				calls++
				if calls <= tc.fails {
					if errors.Is(tc.err, errNotFile) {
						return errNotFile
					}

					return errBusy
				}

				return nil
			}

			err := replace("a", "b", rename,
				func(err error) bool { return errors.Is(err, errBusy) },
				func(d time.Duration) { slept += d })
			if !errors.Is(err, tc.err) || calls != tc.calls {
				t.Errorf("err %v after %d calls, want %v after %d", err, calls, tc.err, tc.calls)
			}

			if slept > time.Second {
				t.Errorf("waited %v", slept)
			}
		})
	}
}

// A symlinked exe.xml stays a symlink; the file it points to is edited.
func TestSetSymlink(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)
	target := filepath.Join(t.TempDir(), "exe.xml")

	err := os.WriteFile(target, []byte(sample), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.Symlink(target, store)
	if err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(store)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v", err)
	}

	if !strings.Contains(readFile(t, target), "<Name>"+entryName+"</Name>") {
		t.Error("the file the symlink points to wasn't edited")
	}
}

// A file that changed after it was read isn't overwritten, nor backed up
// as if this change had started from it.
func TestSaveIfUnchanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "exe.xml")

	err := os.WriteFile(path, []byte("theirs"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = save(path, []byte("before"), []byte("before"), []byte("ours"))
	if !errors.Is(err, errChanged) || readFile(t, path) != "theirs" {
		t.Errorf("err %v, file %q", err, readFile(t, path))
	}

	if left, _ := filepath.Glob(filepath.Join(dir, "exe.xml.*")); len(left) != 0 {
		t.Errorf("backed up although nothing was changed: %q", left)
	}

	err = save(path, []byte("theirs"), []byte("theirs"), []byte("ours"))
	if err != nil || readFile(t, path) != "ours" {
		t.Errorf("err %v, file %q", err, readFile(t, path))
	}
}

// The first backup is kept for good; the latest one is replaced before
// every change, so that another installer's entry, added since the first
// change, can be got back too.
func TestBackupFirstAndPrevious(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)

	err := os.WriteFile(store, []byte(sample), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Another installer adds its entry meanwhile.
	withOther := strings.Replace(readFile(t, store), docClose,
		"<Launch.Addon><Name>Other</Name></Launch.Addon>\r\n"+docClose, 1)

	err = os.WriteFile(store, []byte(withOther), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(false, `C:\x.exe`, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, store+backupExt); got != sample {
		t.Errorf("first backup changed:\n%s", got)
	}

	if got := readFile(t, store+prevExt); got != withOther {
		t.Errorf("latest backup is not the file before the latest change:\n%s", got)
	}
}

// The backup holds the bytes the change was made from, not whatever is in
// the file by the time the backup is written.
func TestBackupUsesBytesRead(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exe.xml")

	err := os.WriteFile(path, []byte("changed by another installer"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = backup(path, []byte(sample))
	if err != nil {
		t.Fatal(err)
	}

	for _, ext := range []string{backupExt, prevExt} {
		if got := readFile(t, path+ext); got != sample {
			t.Errorf("%s: %q", ext, got)
		}
	}
}

func TestBackupPrevNotAFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exe.xml")

	err := os.Mkdir(path+prevExt, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	err = backup(path, []byte(sample))
	if !errors.Is(err, errNotFile) {
		t.Errorf("backup over a directory: %v", err)
	}
}

// Our entry lost its closing tag, and the next add-on's opening tag has a
// space: removing ours must not take the other one with it.
func TestRemoveBrokenEntryKeepsNext(t *testing.T) {
	t.Parallel()

	doc := "<Launch.Addon><Name>SkyDemonConnect</Name><Path>x</Path>" +
		"<Launch.Addon ><Name>A</Name></Launch.Addon>"

	if got := remove(doc); got != doc {
		t.Errorf("remove took another entry:\n%q", got)
	}

	// A well-formed entry of ours after it is still found and removed.
	ours := "<Launch.Addon id=\"2\"><Name>SkyDemonConnect</Name></Launch.Addon >"
	if got := remove(doc + ours); got != doc {
		t.Errorf("our entry with attributes not removed:\n%q", got)
	}
}

// A block with a name other than ours, e.g. two names, is not ours.
func TestOursNeedsOnlyOurName(t *testing.T) {
	t.Parallel()

	for block, want := range map[string]bool{
		"<Launch.Addon><Name>SkyDemonConnect</Name></Launch.Addon>":               true,
		"<Launch.Addon><Name> SkyDemonConnect </Name></Launch.Addon>":             true,
		"<Launch.Addon><Name>SkyDemonConnect</Name><Name>A</Name></Launch.Addon>": false,
		"<Launch.Addon><Path>SkyDemonConnect.exe</Path></Launch.Addon>":           false,
		"<Launch.Addon><Name>SkyDemonConnect Extra</Name></Launch.Addon>":         false,
	} {
		if got := ours(block); got != want {
			t.Errorf("ours(%q) = %v", block, got)
		}
	}
}

// A symlinked exe.xml whose target isn't there is left alone: replacing
// the link with a new file would lose it.
func TestSetBrokenSymlink(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)

	err := os.Symlink(filepath.Join(t.TempDir(), "not-mounted", "exe.xml"), store)
	if err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	err = installs.Set(true, `C:\x.exe`, nil)
	if !errors.Is(err, errBrokenLink) {
		t.Errorf("Set over a broken link: %v", err)
	}

	fi, err := os.Lstat(store)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v %v", fi, err)
	}
}

// A Set that worked for Store but failed for Steam leaves Other, not
// Ours: the next click then completes the entry instead of removing the
// one Store has. Store's file, already ours, isn't written again.
func TestSetRetryAfterPartialFailure(t *testing.T) {
	t.Parallel()

	installs, store, steam := setup(t)
	exe := `C:\x.exe`

	// Store: our entry before another add-on's, so that writing it again
	// would move it to the end.
	ours, err := add(sample, exe, autoArg, cp1252)
	if err != nil {
		t.Fatal(err)
	}

	storeDoc := strings.Replace(ours, "</SimBase.Document>",
		"\t<Launch.Addon>\r\n\t\t<Name>Other</Name>\r\n\t</Launch.Addon>\r\n</SimBase.Document>", 1)

	for path, doc := range map[string]string{store: storeDoc, steam: sample} {
		err = os.WriteFile(path, []byte(doc), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Steam's backup can't be written, so adding to it failed.
	err = os.Mkdir(steam+backupExt, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, exe, nil)
	if !errors.Is(err, errNotFile) {
		t.Fatalf("Set with Steam's backup a folder: %v", err)
	}

	if got := installs.State(exe, nil); got != Other {
		t.Fatalf("entry in one of two files: %v, want Other", got)
	}

	// The backup is fixed; the click enables, as for Other.
	err = os.Remove(steam + backupExt)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(true, exe, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, store); got != storeDoc {
		t.Errorf("Store's file, already ours, was written again:\n%s", got)
	}

	if got := installs.State(exe, nil); got != Ours {
		t.Errorf("after the retry: %v, want Ours", got)
	}
}

// State leaves out a file it can't read, and counts the others.
func TestStateSkipsUnreadable(t *testing.T) {
	t.Parallel()

	installs, store, _ := setup(t)
	exe := `C:\x.exe`

	err := (Installs{store}).Set(true, exe, nil)
	if err != nil {
		t.Fatal(err)
	}

	unreadable := filepath.Join(t.TempDir(), "exe.xml")

	err = os.Mkdir(unreadable, 0o750) // reading a directory fails
	if err != nil {
		t.Fatal(err)
	}

	if got := (Installs{unreadable, store}).State(exe, nil); got != Ours {
		t.Errorf("with an unreadable file: %v, want Ours", got)
	}

	if got := (Installs{unreadable}).State(exe, nil); got != Absent {
		t.Errorf("only an unreadable file: %v, want Absent", got)
	}

	if got := installs.State(exe, nil); got != Other {
		t.Errorf("in Store only: %v, want Other", got)
	}
}

// A file that wasn't there is backed up as one without entries: the
// "original" never holds our entry, also after a later disable.
func TestBackupOfNewFile(t *testing.T) {
	t.Parallel()

	_, store, _ := setup(t)
	installs := Installs{store}

	for _, enable := range []bool{true, false} {
		err := installs.Set(enable, `C:\x.exe`, nil)
		if err != nil {
			t.Fatal(err)
		}
	}

	if got := readFile(t, store+backupExt); got != skeleton {
		t.Errorf("first backup:\n%s", got)
	}

	// The latest backup is from before the disable, with the entry.
	if got := readFile(t, store+prevExt); !strings.Contains(got, entryName) {
		t.Errorf("latest backup:\n%s", got)
	}
}

// Nothing to do writes nothing: no file, no backup.
func TestSetNoChange(t *testing.T) {
	t.Parallel()

	_, store, _ := setup(t)
	installs := Installs{store}

	// Removing from a file without our entry.
	err := os.WriteFile(store, []byte(sample), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = installs.Set(false, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	left, _ := filepath.Glob(store + ".*")
	if len(left) != 0 || readFile(t, store) != sample {
		t.Errorf("removing nothing wrote %q", left)
	}
}

// A file Set couldn't add to, e.g. one without </SimBase.Document>, is
// left out of State: otherwise the entry in the other one would read as
// Other for good, and a click could never remove it.
func TestStateLeavesOutRefusedFile(t *testing.T) {
	t.Parallel()

	installs, store, steam := setup(t)
	exe := `C:\x.exe`

	err := os.WriteFile(steam, []byte("<foo/>"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = (Installs{store}).Set(true, exe, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := installs.State(exe, nil); got != Ours {
		t.Fatalf("with a file Set refuses: %v, want Ours", got)
	}

	err = installs.Set(false, exe, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := installs.State(exe, nil); got != Absent {
		t.Errorf("after removing: %v, want Absent", got)
	}
}

// Two entries of ours, e.g. one left from an older version after the
// current one, read as Other, and Set leaves one.
func TestDuplicateEntry(t *testing.T) {
	t.Parallel()

	_, store, _ := setup(t)
	installs := Installs{store}
	exe := `C:\x.exe`

	ours, err := add(sample, exe, autoArg, cp1252)
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Replace(ours, "</SimBase.Document>",
		"\t<Launch.Addon>\r\n\t\t<Name>SkyDemonConnect</Name>\r\n"+
			"\t\t<Path>C:\\Old\\SkyDemonConnect.exe</Path>\r\n\t</Launch.Addon>\r\n</SimBase.Document>", 1)

	err = os.WriteFile(store, []byte(old), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if got := installs.State(exe, nil); got != Other {
		t.Errorf("two entries: %v, want Other", got)
	}

	err = installs.Set(true, exe, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, store); got != ours {
		t.Errorf("after Set:\n%s", got)
	}
}

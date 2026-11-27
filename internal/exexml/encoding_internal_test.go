package exexml

import (
	"bytes"
	"strings"
	"testing"
)

const decl1252 = `<?xml version="1.0" encoding="Windows-1252"?>` + "\r\n"

func TestEncodingRoundTrip(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input []byte
		want  encoding
	}{
		{"windows-1252", []byte(decl1252 + "<SimBase.Document>\r\n</SimBase.Document>\r\n"), cp1252},
		{
			"utf-8 bom", append(
				[]byte{0xEF, 0xBB, 0xBF},
				"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<SimBase.Document>\n</SimBase.Document>\n"...),
			utf8WithBOM,
		},
		{"1252 with utf-8 inside", []byte(decl1252 +
			"<SimBase.Document><Launch.Addon><Name>Sp\xc3\xa4d</Name></Launch.Addon>\r\n" +
			"</SimBase.Document>\r\n"), cp1252},
		{"plain utf-8", []byte("<?xml version=\"1.0\"?>\n" +
			"<SimBase.Document><Launch.Addon><Name>\xc3\xa4</Name></Launch.Addon>\n" +
			"</SimBase.Document>\n"), utf8Plain},
		{
			"invalid utf-8, no declaration", []byte("<SimBase.Document>\xe4\n</SimBase.Document>\n"),
			bytewise,
		},
		// Only the declaration counts, not a mention of 1252 elsewhere.
		{
			"utf-8, comment mentions 1252", []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
				"<!-- was Windows-1252, iso-8859-1 -->\n<SimBase.Document>\n</SimBase.Document>\n"),
			utf8Plain,
		},
		{"single quotes", []byte("<?xml version='1.0' encoding='cp1252'?>\n" +
			"<SimBase.Document>\n</SimBase.Document>\n"), cp1252},
		// Another declared encoding, even if all ASCII, gets only ASCII.
		{"declared windows-1250", []byte("<?xml version=\"1.0\" encoding=\"Windows-1250\"?>\n" +
			"<SimBase.Document>\n</SimBase.Document>\n"), bytewise},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			roundTrip(t, tc.input, tc.want)
		})
	}
}

// roundTrip checks that input decodes as want, encodes back byte for byte,
// and that adding and removing our entry restores it.
func roundTrip(t *testing.T, input []byte, want encoding) {
	t.Helper()

	doc, enc, err := decode(input)
	if err != nil {
		t.Fatal(err)
	}

	if enc != want {
		t.Errorf("encoding %v, want %v", enc, want)
	}

	out, err := enc.encode(doc)
	if err != nil || !bytes.Equal(out, input) {
		t.Errorf("not byte-identical (err %v)", err)
	}

	added, err := edited(doc, true, `C:\Users\Jürgen\SkyDemonConnect.exe`, autoArg, enc)
	if err != nil {
		t.Fatal(err)
	}

	b, err := enc.encode(added)
	if err != nil {
		t.Fatal(err)
	}

	checkPath(t, b, enc)

	doc2, enc2, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}

	if enc2 != enc {
		t.Errorf("encoding changed from %v to %v", enc, enc2)
	}

	if back, _ := enc.encode(remove(doc2)); !bytes.Equal(back, input) {
		t.Error("add+remove changed the file")
	}

	if enc == utf8WithBOM && !strings.HasPrefix(string(b), "\xef\xbb\xbf") {
		t.Error("BOM lost")
	}
}

// checkPath checks how ü of the added path was written.
func checkPath(t *testing.T, b []byte, enc encoding) {
	t.Helper()

	want := map[encoding]string{
		cp1252:      "J\xfcrgen",
		bytewise:    "J&#xFC;rgen",
		utf8Plain:   "J\xc3\xbcrgen",
		utf8WithBOM: "J\xc3\xbcrgen",
	}[enc]
	if !bytes.Contains(b, []byte(want)) {
		t.Errorf("ü not written as %q:\n%q", want, b)
	}
}

// Every byte of a Windows-1252 file reads back as it was, also the five
// that Windows-1252 leaves undefined.
func TestCP1252EveryByte(t *testing.T) {
	t.Parallel()

	all := make([]byte, 0, 256)
	for c := range 256 {
		all = append(all, byte(c))
	}

	b, err := cp1252.encode(decode1252(all))
	if err != nil || !bytes.Equal(b, all) {
		t.Errorf("round trip: %v, %x", err, b)
	}

	if got := decode1252([]byte{0x80, 0x8A, 0x9F, 0xE9}); got != "€ŠŸé" {
		t.Errorf("decoded %q", got)
	}
}

// An entry written by hand, or by an older version, with Š in its path
// as Windows-1252's byte is ours.
func TestCP1252EntryByHand(t *testing.T) {
	t.Parallel()

	const exe = `C:\Users\Šárka\SkyDemonConnect.exe`

	file := strings.Replace(sample, "</SimBase.Document>",
		"\t<Launch.Addon>\r\n\t\t<Name>SkyDemonConnect</Name>\r\n"+
			"\t\t<Path>C:\\Users\\\x8a\xe1rka\\SkyDemonConnect.exe</Path>\r\n"+
			"\t\t<CommandLine>-auto</CommandLine>\r\n\t</Launch.Addon>\r\n</SimBase.Document>", 1)

	doc, enc, err := decode([]byte(file))
	if err != nil || enc != cp1252 {
		t.Fatalf("decode: %v, %v", enc, err)
	}

	if got := entryState(doc, exe, autoArg); got != Ours {
		t.Errorf("entry by hand: %v, want Ours", got)
	}
}

// A file declared ISO-8859-1 isn't Windows-1252: its bytes 0x80 to 0x9F
// are kept as they are, and a character Windows-1252 has there, e.g. Ž,
// is written as a reference, not as 0x8E, which ISO-8859-1 reads as a
// control character.
func TestLatin1IsNot1252(t *testing.T) {
	t.Parallel()

	const declLatin1 = `<?xml version="1.0" encoding="ISO-8859-1"?>` + "\r\n"

	roundTrip(
		t,
		[]byte(declLatin1+"<SimBase.Document><!-- \x8e\xe9 -->\r\n</SimBase.Document>\r\n"),
		latin1,
	)

	for _, tc := range []struct {
		decl string
		want string
	}{
		{declLatin1, `<Path>C:\&#x17D;\&#x20AC;\` + "\xe9" + `\x.exe</Path>`},
		{decl1252, `<Path>C:\` + "\x8e" + `\` + "\x80" + `\` + "\xe9" + `\x.exe</Path>`},
	} {
		doc, enc, err := decode([]byte(tc.decl + "<SimBase.Document>\r\n</SimBase.Document>\r\n"))
		if err != nil {
			t.Fatal(err)
		}

		out, err := add(doc, `C:\Ž\€\é\x.exe`, autoArg, enc)
		if err != nil {
			t.Fatal(err)
		}

		b, err := enc.encode(out)
		if err != nil || !strings.Contains(string(b), tc.want) {
			t.Errorf("%s: %v, no %q in:\n%s", tc.decl, err, tc.want, b)
		}

		// What was written reads back as ours.
		back, _, err := decode(b)
		if err != nil || entryState(back, `C:\Ž\€\é\x.exe`, autoArg) != Ours {
			t.Errorf("%s: not ours after reading back (%v)", tc.decl, err)
		}
	}
}

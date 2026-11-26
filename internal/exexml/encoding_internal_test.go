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
		{"windows-1252", []byte(decl1252 + "<SimBase.Document>\r\n</SimBase.Document>\r\n"), latin1},
		{
			"utf-8 bom", append(
				[]byte{0xEF, 0xBB, 0xBF},
				"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<SimBase.Document>\n</SimBase.Document>\n"...),
			utf8WithBOM,
		},
		{"1252 with utf-8 inside", []byte(decl1252 +
			"<SimBase.Document><Launch.Addon><Name>Sp\xc3\xa4d</Name></Launch.Addon>\r\n" +
			"</SimBase.Document>\r\n"), latin1},
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
			"<SimBase.Document>\n</SimBase.Document>\n"), latin1},
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
		latin1:      "J\xfcrgen",
		bytewise:    "J&#xFC;rgen",
		utf8Plain:   "J\xc3\xbcrgen",
		utf8WithBOM: "J\xc3\xbcrgen",
	}[enc]
	if !bytes.Contains(b, []byte(want)) {
		t.Errorf("ü not written as %q:\n%q", want, b)
	}
}

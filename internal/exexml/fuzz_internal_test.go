package exexml

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// splitArgv splits a Windows command line as CommandLineToArgvW and Go's
// runtime do: 2n backslashes before a quote are n backslashes, 2n+1 are n
// and a literal quote, other backslashes are literal, and quotes group.
func splitArgv(cmd string) []string {
	var args []string

	for cmd != "" {
		if cmd[0] == ' ' || cmd[0] == '\t' {
			cmd = cmd[1:]

			continue
		}

		var arg strings.Builder

		quoted := false

		for cmd != "" && (quoted || cmd[0] != ' ' && cmd[0] != '\t') {
			switch cmd[0] {
			case '\\':
				n := len(cmd) - len(strings.TrimLeft(cmd, `\`))
				if n < len(cmd) && cmd[n] == '"' {
					arg.WriteString(strings.Repeat(`\`, n/2))

					cmd = cmd[n:]
					if n%2 == 1 {
						arg.WriteByte('"')

						cmd = cmd[1:]
					}

					continue
				}

				arg.WriteString(cmd[:n])

				cmd = cmd[n:]
			case '"':
				quoted = !quoted
				cmd = cmd[1:]
			default:
				arg.WriteByte(cmd[0])

				cmd = cmd[1:]
			}
		}

		args = append(args, arg.String())
	}

	return args
}

// The command line reads back as the options it was made of.
func FuzzCommandLine(f *testing.F) {
	for _, seed := range [][2]string{
		{"-udp=auto", "-name=My Sim"},
		{"", `-name=a"b`},
		{`-name=a\ b\`, `\\"`},
		{"\t", `"\"`},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, a, b string) {
		// Options come from os.Args, which Windows gives as UTF-16: valid
		// UTF-8 once Go converted them, and without NUL.
		if !utf8.ValidString(a) || !utf8.ValidString(b) || strings.ContainsRune(a+b, 0) {
			t.Skip("not an option Windows can pass")
		}

		args := []string{a, b}

		got := splitArgv(commandLine(args))
		if want := append([]string{autoArg}, args...); !slices.Equal(got, want) {
			t.Errorf("%q reads back as %q, want %q", commandLine(args), got, want)
		}
	})
}

// Adding our entry to any file makes it ours, and removing it gives back
// the file without it, byte for byte.
func FuzzAddRemove(f *testing.F) {
	for _, seed := range []string{
		sample,
		skeleton,
		"<SimBase.Document></SimBase.Document>",
		"<SimBase.Document><Launch.Addon><Name>SkyDemonConnect</Name>\n" +
			"<Launch.Addon ><Name>Other</Name></Launch.Addon></SimBase.Document>",
		"<SimBase.Document><!-- <Launch.Addon><Name>SkyDemonConnect</Name>" +
			"</Launch.Addon> -->\r\n</SimBase.Document>\r\n",
	} {
		f.Add(seed, `C:\Tools\SkyDemonConnect.exe`)
	}

	f.Fuzz(func(t *testing.T, doc, exe string) {
		// A path as Windows has it: no characters XML text can't hold, nor
		// the leading or trailing spaces entryState trims.
		if exe == "" || !utf8.ValidString(exe) || strings.TrimSpace(exe) != exe ||
			strings.ContainsFunc(exe, func(r rune) bool { return xmlRune(r) != r || r == '\r' }) {
			t.Skip("not a path")
		}

		out, err := add(doc, exe, autoArg, utf8Plain)
		if err != nil {
			return // e.g. no </SimBase.Document>
		}

		if got := entryState(out, exe, autoArg); got != Ours {
			t.Fatalf("after add: %v, want Ours:\n%s", got, out)
		}

		if got, want := remove(out), remove(doc); got != want {
			t.Errorf("remove(add(doc)) = %q, want remove(doc) = %q", got, want)
		}
	})
}

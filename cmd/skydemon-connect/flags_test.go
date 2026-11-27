package main

import (
	"errors"
	"flag"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/serial"
)

func TestParseFlags(t *testing.T) {
	t.Parallel()

	c, _, err := parseFlags(nil)
	if err != nil || c.udp != "auto" || c.baud != 115200 || c.name != "MSFS" {
		t.Fatalf("defaults: %+v %v", c, err)
	}

	c, _, err = parseFlags([]string{"-udp", "127.0.0.1:49002", "-com", "COM11", "-auto"})
	if err != nil || c.udp != "127.0.0.1:49002" || c.com != "COM11" || !c.auto {
		t.Errorf("flags: %+v %v", c, err)
	}

	_, usage, err := parseFlags([]string{"-hz", "10"}) // removed: always 30 Hz
	if err == nil || !strings.HasPrefix(usage, "flag provided but not defined: -hz\n") ||
		!strings.HasSuffix(usage, "Run with -help to see the options.") {
		t.Errorf("unknown flag: %v, usage %q", err, usage)
	}

	// The help is for a message box: no tab alignment, every option.
	_, usage, err = parseFlags([]string{"-help"})
	if !errors.Is(err, flag.ErrHelp) || strings.Contains(usage, "\t") ||
		!strings.Contains(usage, "\n-baud (default 115200):\n    COM port baud rate") ||
		!strings.Contains(usage, "\n-udp (default auto):\n") ||
		!strings.Contains(usage, "\n-version:\n") {
		t.Errorf("-help: %v\n%s", err, usage)
	}

	c, _, err = parseFlags([]string{"-version"})
	if err != nil || !c.version {
		t.Errorf("-version: %v %+v", err, c)
	}
}

func TestParseFlagsValidates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		want error
		args []string
	}{
		{errArg, []string{"-udp", "192.168.1.20:49002,", "192.168.1.21:49002"}},
		{serial.ErrBaud, []string{"-com", "COM1", "-baud", "0"}},
		{serial.ErrBaud, []string{"-com", "COM1", "-baud", "4294967296"}},
		{errBaudNoCom, []string{"-baud", "9600"}},
		{errNoOut, []string{"-udp="}},
		{errNoOut, []string{"-udp", " , "}},
		{errName, []string{"-name", "MS,FS"}},
		{errName, []string{"-name="}},
		{errName, []string{"-name", "Flugsimulator für zu Hause"}},
		{errName, []string{"-name", strings.Repeat("x", maxName+1)}},
		{errDemoLat, []string{"-demo-lat", "90"}},
		{errDemoLat, []string{"-demo-lat", "NaN"}},
		{errDemoLon, []string{"-demo-lon", "-181"}},
	} {
		_, usage, err := parseFlags(tc.args)
		if !errors.Is(err, tc.want) ||
			!strings.Contains(strings.SplitN(usage, "\n", 2)[0], tc.want.Error()) ||
			!strings.Contains(usage, "-help") {
			t.Errorf("%v: %v, usage %q", tc.args, err, usage)
		}
	}
}

// -quiet only changes the console's status line; outside Windows the
// console mode is the only one.
func TestParseFlagsQuiet(t *testing.T) {
	t.Parallel()

	_, _, err := parseFlags([]string{"-quiet"})
	if want := runtime.GOOS == "windows"; errors.Is(err, errQuiet) != want {
		t.Errorf("-quiet alone: %v", err)
	}

	_, _, err = parseFlags([]string{"-quiet", "-console"})
	if err != nil {
		t.Errorf("-quiet -console: %v", err)
	}
}

// A bad -udp target is a usage error, not one at startup.
func TestParseFlagsValidatesUDP(t *testing.T) {
	t.Parallel()

	_, usage, err := parseFlags([]string{"-udp", "192.168.1.300:49002"})
	if err == nil || !strings.HasPrefix(usage, "-udp: ") {
		t.Errorf("%v, usage %q", err, usage)
	}
}

func TestInsideArchive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		dir  string
		want bool
	}{
		{`C:\Users\a\AppData\Local\Temp\Temp1_SkyDemonConnect-v1.0.0.zip`, true},
		{`C:\Users\a\AppData\Local\Temp\Temp1_x.ZIP\SkyDemonConnect`, true},
		{`C:\Users\a\AppData\Local\Temp\7zO4C1B2A3D`, true},
		{`C:\Users\a\AppData\Local\Temp\Rar$EXa1234.5678`, true},
		{`C:\Tools\SkyDemonConnect`, false},
		{`C:\Users\a\AppData\Local\Temp\go-build123\b001\exe`, false}, // go run
		{`/tmp/zip/skydemon`, false},
	} {
		if got := insideArchive(tc.dir); got != tc.want {
			t.Errorf("insideArchive(%q) = %v", tc.dir, got)
		}
	}
}

// flags returns the config for args, as parseFlags makes it.
func flags(t *testing.T, args ...string) *config {
	t.Helper()

	c, usage, err := parseFlags(args)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, usage)
	}

	return &c
}

func TestPassOn(t *testing.T) {
	t.Parallel()

	c := flags(t, "-auto", "-console", "-demo", "-udp", "auto,192.168.1.23:49002",
		"-name", `My "Sim"`, "-com=COM11", "-baud", "115200")

	// Raw, unquoted; -baud at its default is left out.
	want := []string{`-com=COM11`, `-name=My "Sim"`, `-udp=auto,192.168.1.23:49002`}
	if !slices.Equal(c.passOn, want) {
		t.Errorf("passOn %q, want %q", c.passOn, want)
	}

	if c := flags(t, "-udp=auto"); c.passOn != nil {
		t.Errorf("an option at its default: passOn %q", c.passOn)
	}
}

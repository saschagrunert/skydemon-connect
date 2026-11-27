package main

import (
	"errors"
	"flag"
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
	if err == nil || !strings.Contains(usage, "-udp") ||
		strings.Count(usage, "flag provided but not defined") != 1 {
		t.Errorf("unknown flag: %v, usage %q", err, usage)
	}

	_, usage, err = parseFlags([]string{"-help"})
	if !errors.Is(err, flag.ErrHelp) || usage == "" {
		t.Errorf("-help: %v", err)
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
		{serial.ErrBaud, []string{"-baud", "0"}},
		{serial.ErrBaud, []string{"-baud", "4294967296"}},
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
			!strings.Contains(usage, "-udp") {
			t.Errorf("%v: %v, usage %q", tc.args, err, usage)
		}
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

package targets_test

import (
	"io"
	"log"
	"net/netip"
	"strings"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/targets"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func TestParse(t *testing.T) {
	t.Parallel()

	s, err := targets.Parse(
		" 127.0.0.1:49002 , ,10.0.0.255:4000,127.0.0.1:49002",
		49002,
	)
	if err != nil {
		t.Fatal(err)
	}

	if s.Empty() || s.Dynamic() {
		t.Errorf("fixed list: empty %v dynamic %v", s.Empty(), s.Dynamic())
	}

	if got := s.Resolver(testutil.Log(t)).
		Resolve(t.Context()).
		String(); got != "127.0.0.1:49002, 10.0.0.255:4000" {
		t.Errorf("resolve: %s", got)
	}

	if s, _ := targets.Parse("", 1); !s.Empty() {
		t.Error("empty list not empty")
	}
}

func TestParseDoesNotLookUp(t *testing.T) {
	t.Parallel()

	// A tablet that is off at startup must not stop the feed: host names
	// are only looked up by Resolve.
	s, err := targets.Parse("nosuchhost.invalid:5000", 49002)
	if err != nil {
		t.Fatal(err)
	}

	if !s.Dynamic() || s.Empty() {
		t.Error("a host name is resolved again, so the spec is dynamic")
	}

	if d := s.Describe(); d != "nosuchhost.invalid:5000" {
		t.Errorf("describe: %s", d)
	}
}

func TestAuto(t *testing.T) {
	t.Parallel()

	s, err := targets.Parse("AUTO,192.0.2.1:5000,127.0.0.1:49002", 49002)
	if err != nil {
		t.Fatal(err)
	}

	if !s.Dynamic() || s.Empty() {
		t.Fatal("auto not detected")
	}

	// Fixed targets first, then this PC once although listed twice, then
	// whatever broadcast addresses this machine has.
	got := s.Resolver(testutil.Log(t)).Resolve(t.Context())
	if len(got) < 2 || got[0].String() != "192.0.2.1:5000" || got[1].String() != "127.0.0.1:49002" {
		t.Errorf("resolve: %s", got)
	}

	if n := strings.Count(got.String(), "127.0.0.1:49002"); n != 1 {
		t.Errorf("loopback listed %d times: %s", n, got)
	}

	for _, a := range got[2:] {
		if a.Port() != 49002 || !a.Addr().Is4() {
			t.Errorf("broadcast target %v: want IPv4 on port 49002", a)
		}
	}

	if d := s.Describe(); !strings.HasPrefix(
		d,
		"auto (this PC + local networks, port 49002), 192.0.2.1:5000",
	) {
		t.Errorf("describe: %s", d)
	}
}

// A target without a port gets the default one, a host name too.
func TestParseDefaultPort(t *testing.T) {
	t.Parallel()

	s, err := targets.Parse("192.168.1.23,ipad.local", 49002)
	if err != nil {
		t.Fatal(err)
	}

	if d := s.Describe(); d != "192.168.1.23:49002, ipad.local:49002" {
		t.Errorf("describe: %s", d)
	}
}

func TestFormat(t *testing.T) {
	t.Parallel()

	l := targets.List{
		netip.MustParseAddrPort("127.0.0.1:49002"),
		netip.MustParseAddrPort("192.168.1.23:4000"),
	}

	for omit, want := range map[uint16]string{
		0:     "127.0.0.1:49002, 192.168.1.23:4000",
		49002: "127.0.0.1, 192.168.1.23:4000",
	} {
		if got := l.Format(omit); got != want {
			t.Errorf("Format(%d) = %q, want %q", omit, got, want)
		}
	}

	if got := l.String(); got != l.Format(0) {
		t.Errorf("String %q", got)
	}

	if got := (targets.List{}).Format(49002); got != "" {
		t.Errorf("empty: %q", got)
	}
}

// Parse never panics, and what it accepts describes and resolves without
// a lookup for literal addresses.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"auto", "auto,192.168.1.23:49002", "ipad.local", "127.0.0.1:0", "[::1]:49002",
		" , ", "auto:4000", "a-b.c:65535", "1.2.3.4:5:6", "192.168.1.300",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, list string) {
		s, err := targets.Parse(list, 49002)
		if err != nil {
			return
		}

		_ = s.Describe()

		if !s.Dynamic() {
			for _, a := range s.Resolver(log.New(io.Discard, "", 0)).Known() {
				if !a.Addr().Is4() || a.Port() == 0 {
					t.Errorf("%q: target %v", list, a)
				}
			}
		}
	})
}

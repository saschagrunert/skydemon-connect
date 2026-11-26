package targets_test

import (
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

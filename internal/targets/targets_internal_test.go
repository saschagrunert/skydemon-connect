package targets

import (
	"context"
	"errors"
	"log"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func ipNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()

	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}

	n.IP = ip

	return n
}

func TestBroadcasts(t *testing.T) {
	t.Parallel()

	sixteen := ipNet(t, "192.168.7.7/24")
	sixteen.IP = sixteen.IP.To16()
	sixteen.Mask = net.CIDRMask(96+24, 128) // IPv4 with a 16-byte mask

	addrs := []net.Addr{
		ipNet(t, "192.168.1.23/24"),
		ipNet(t, "10.10.5.7/16"),
		ipNet(t, "172.20.3.4/20"),
		ipNet(t, "192.168.5.1/30"),
		sixteen,
		ipNet(t, "169.254.7.7/16"),            // link-local: skipped
		ipNet(t, "127.0.0.1/8"),               // loopback: skipped
		ipNet(t, "192.168.9.9/32"),            // no broadcast: skipped
		ipNet(t, "192.168.9.8/31"),            // point-to-point: skipped
		ipNet(t, "fe80::1/64"),                // IPv6: skipped
		&net.IPAddr{IP: net.IPv4(1, 2, 3, 4)}, // not a network: skipped
	}

	got := make([]string, 0, len(addrs))
	for _, ip := range broadcasts(addrs) {
		got = append(got, ip.String())
	}

	want := "192.168.1.255 10.10.255.255 172.20.15.255 192.168.5.3 192.168.7.255"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

var errNoSuchHost = errors.New("no such host")

func TestParseErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		target string
		want   error
	}{
		{"not an address", errBadHost},
		{"1.2.3.4:5:6", errSyntax},
		{"[1.2.3.4:5", errSyntax},
		{"127.0.0.1:0", errNoPort},
		{"127.0.0.1:65536", errNoPort},
		{"[::1]:49002", errNotIPv4},
		{":49002", errNoHost},
		{"tablet:http", errNoPort},          // port by name
		{"192.168.1.300:49002", errBadHost}, // a mistyped IP, not a host name
		{"192.168.1:49002", errBadHost},
		{"ipad .local:49002", errBadHost},
		{"-ipad.local:49002", errBadHost},
		{"auto:4000", errAutoPort},
		{"AUTO:4000", errAutoPort},
	} {
		_, err := Parse(tc.target, 49002)
		if !errors.Is(err, tc.want) {
			t.Errorf("%q: %v, want %v", tc.target, err, tc.want)
		}
	}
}

func TestHostNameFollowed(t *testing.T) {
	t.Parallel()

	var logs testutil.Buffer

	spec, err := Parse("tablet.local:5000,192.0.2.1:6000", 49002)
	if err != nil {
		t.Fatal(err)
	}

	var answer []netip.Addr

	s := spec.Resolver(log.New(&logs, "", 0))
	s.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "tablet.local" {
			t.Errorf("lookup of %q", host)
		}

		if answer == nil {
			return nil, errNoSuchHost
		}

		return answer, nil
	}

	for _, step := range []struct {
		answer []netip.Addr
		want   string
		logged int
	}{
		{nil, "192.0.2.1:6000", 1},
		{nil, "192.0.2.1:6000", 1}, // the same error: not logged again
		{[]netip.Addr{netip.MustParseAddr("::1")}, "192.0.2.1:6000", 2},
		{
			[]netip.Addr{netip.MustParseAddr("fe80::1"), netip.MustParseAddr("192.168.1.9")},
			"192.0.2.1:6000, 192.168.1.9:5000",
			3,
		},
		{[]netip.Addr{netip.MustParseAddr("192.168.1.9")}, "192.0.2.1:6000, 192.168.1.9:5000", 3},
		// Failed lookups keep the last address keepFailed times, then
		// drop it.
		{nil, "192.0.2.1:6000, 192.168.1.9:5000", 4},
		{nil, "192.0.2.1:6000, 192.168.1.9:5000", 4},
		{nil, "192.0.2.1:6000, 192.168.1.9:5000", 4},
		{nil, "192.0.2.1:6000", 4},
		{nil, "192.0.2.1:6000", 4},
		// A success starts the count over.
		{[]netip.Addr{netip.MustParseAddr("192.168.1.10")}, "192.0.2.1:6000, 192.168.1.10:5000", 5},
		{nil, "192.0.2.1:6000, 192.168.1.10:5000", 6},
	} {
		answer = step.answer

		if got := s.Resolve(t.Context()).String(); got != step.want {
			t.Errorf("answer %v: resolve %s, want %s", step.answer, got, step.want)
		}

		if n := strings.Count(logs.String(), "\n"); n != step.logged {
			t.Errorf(
				"answer %v: %d log lines, want %d:\n%s",
				step.answer,
				n,
				step.logged,
				logs.String(),
			)
		}
	}

	if !strings.Contains(logs.String(), "targets: tablet.local:5000: working again") {
		t.Errorf("log %q", logs.String())
	}
}

// TestResolveCancelled checks that a lookup cut short by shutdown is not
// logged as a failure.
func TestResolveCancelled(t *testing.T) {
	t.Parallel()

	var logs testutil.Buffer

	spec, err := Parse("tablet.local:5000", 49002)
	if err != nil {
		t.Fatal(err)
	}

	s := spec.Resolver(log.New(&logs, "", 0))
	s.lookup = func(ctx context.Context, _ string) ([]netip.Addr, error) { return nil, ctx.Err() }

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if got := s.Resolve(ctx); len(got) != 0 || logs.String() != "" {
		t.Errorf("resolve %v, log %q", got, logs.String())
	}
}

// Host names are looked up in parallel: two that take the whole timeout
// together take it once.
func TestResolveParallel(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		spec, err := Parse("a.local:1,b.local:2,192.0.2.1:3", 49002)
		if err != nil {
			t.Fatal(err)
		}

		r := spec.Resolver(testutil.Log(t))
		r.lookup = func(context.Context, string) ([]netip.Addr, error) {
			time.Sleep(lookupTimeout)

			return []netip.Addr{netip.MustParseAddr("192.0.2.9")}, nil
		}

		if got := r.Known().String(); got != "192.0.2.1:3" {
			t.Errorf("known before any lookup: %s", got)
		}

		start := time.Now()
		got := r.Resolve(t.Context())

		if d := time.Since(start); d != lookupTimeout {
			t.Errorf("two lookups took %v, want %v", d, lookupTimeout)
		}

		if got.String() != "192.0.2.1:3, 192.0.2.9:1, 192.0.2.9:2" {
			t.Errorf("resolve: %s", got)
		}
	})
}

// The real lookup, with a name every system resolves.
func TestLookupIPv4(t *testing.T) {
	t.Parallel()

	addrs, err := lookupIPv4(t.Context(), "localhost")
	if err != nil {
		t.Skipf("no localhost on this system: %v", err)
	}

	if !slices.ContainsFunc(addrs, func(a netip.Addr) bool { return a.Unmap().IsLoopback() }) {
		t.Errorf("localhost: %v", addrs)
	}
}

// auto broadcasts on Wi-Fi and Ethernet, not on loopback or tunnels.
func TestBroadcastAdapter(t *testing.T) {
	t.Parallel()

	const running = net.FlagUp | net.FlagRunning

	for _, tc := range []struct {
		flags net.Flags
		want  bool
	}{
		{running | net.FlagBroadcast, true},
		{running | net.FlagBroadcast | net.FlagMulticast, true},
		{net.FlagUp | net.FlagBroadcast, false}, // not running
		{running, false},                        // no broadcast
		{running | net.FlagBroadcast | net.FlagLoopback, false},
		{running | net.FlagBroadcast | net.FlagPointToPoint, false}, // a VPN
	} {
		if got := broadcastAdapter(tc.flags); got != tc.want {
			t.Errorf("broadcastAdapter(%v) = %v", tc.flags, got)
		}
	}
}

// A host name with only IPv6 addresses says so, not "not an IPv4 address",
// which is about literal addresses.
func TestResolveNoIPv4(t *testing.T) {
	t.Parallel()

	spec, err := Parse("tablet.local:5000", 49002)
	if err != nil {
		t.Fatal(err)
	}

	r := spec.Resolver(testutil.Log(t))
	r.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("fe80::1")}, nil
	}

	_, err = r.resolve(t.Context(), spec.hosts[0])
	if !errors.Is(err, errNoIPv4) {
		t.Errorf("resolve: %v", err)
	}
}

// Refresh sets the targets at once, for the host names Known leaves out,
// and then each time they change, checking every interval.
func TestRefresh(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		spec, err := Parse("127.0.0.1:1,tablet.local:2", 49002)
		if err != nil {
			t.Fatal(err)
		}

		calls := 0
		r := spec.Resolver(testutil.Log(t))
		r.lookup = func(context.Context, string) ([]netip.Addr, error) {
			calls++
			if calls < 3 {
				return nil, errNoSuchHost
			}

			return []netip.Addr{netip.MustParseAddr("127.0.0.2")}, nil
		}

		var sets []string

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})

		go func() {
			defer close(done)

			r.Refresh(ctx, time.Second, r.Known(), func(l []netip.AddrPort) {
				sets = append(sets, List(l).String())
			})
		}()

		time.Sleep(5*time.Second + time.Second/2)
		synctest.Wait()
		cancel()
		<-done

		// Once at the start, then every interval; one change.
		if calls != 6 || !slices.Equal(sets, []string{"127.0.0.1:1, 127.0.0.2:2"}) {
			t.Errorf("%d lookups, sets %q", calls, sets)
		}
	})
}

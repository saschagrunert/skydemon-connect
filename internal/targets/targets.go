// Package targets turns the -udp option into UDP destinations. The keyword
// "auto" stands for this PC plus the broadcast address of every local
// network, so that tablets on the same Wi-Fi receive the feed too.
package targets

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/report"
)

// Auto is the keyword for this PC plus every local network.
const Auto = "auto"

var (
	errSyntax   = errors.New("not host:port, e.g. 192.168.1.23:49002")
	errAutoPort = errors.New(`"auto" takes no port; it always uses 49002`)
	errNoHost   = errors.New("missing host")
	errBadHost  = errors.New("not an IPv4 address or a host name")
	errNoPort   = errors.New("port must be 1-65535")
	errNotIPv4  = errors.New("not an IPv4 address")
)

// lookupTimeout bounds one host name lookup, so that a slow DNS server
// doesn't hold up the next network check.
const lookupTimeout = 2 * time.Second

// List is a list of UDP destinations.
type List []netip.AddrPort

// String lists the destinations for the log.
func (l List) String() string {
	s := make([]string, len(l))
	for i, a := range l {
		s[i] = a.String()
	}

	return strings.Join(s, ", ")
}

// lookupFunc returns the addresses of a host name.
type lookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)

// hostPort is a host name target.
type hostPort struct {
	name string
	port uint16
}

func (h hostPort) String() string {
	return net.JoinHostPort(h.name, strconv.Itoa(int(h.port)))
}

// Spec is a parsed -udp option. It doesn't change after Parse.
type Spec struct {
	// fixed are literal addresses. hosts are names, looked up on each
	// Resolve, so that a tablet that is off at startup, or whose DHCP
	// address changes, is followed.
	fixed List
	hosts []hostPort
	port  uint16
	auto  bool
}

// Parse parses a comma-separated list of host:port targets and the Auto
// keyword. Auto targets use port. Host names are only checked for syntax
// here; a Resolver looks them up.
func Parse(list string, port uint16) (*Spec, error) {
	spec := &Spec{port: port}

	for t := range strings.SplitSeq(list, ",") {
		t = strings.TrimSpace(t)

		switch {
		case t == "":
		case strings.EqualFold(t, Auto):
			spec.auto = true
		case strings.HasPrefix(strings.ToLower(t), Auto+":"):
			return nil, fmt.Errorf("bad target %q: %w", t, errAutoPort)
		default:
			err := spec.add(t)
			if err != nil {
				return nil, fmt.Errorf("bad target %q: %w", t, err)
			}
		}
	}

	return spec, nil
}

func lookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

// Empty reports whether the spec sends nowhere.
func (s *Spec) Empty() bool { return !s.auto && len(s.fixed) == 0 && len(s.hosts) == 0 }

// Dynamic reports whether Resolve depends on the network configuration
// or on name resolution.
func (s *Spec) Dynamic() bool { return s.auto || len(s.hosts) > 0 }

// Resolver turns a Spec into destinations, again and again as the network
// changes. It may be used concurrently.
type Resolver struct {
	spec   *Spec
	lookup lookupFunc
	// errs logs each host name's lookup failures once per change.
	errs *report.Reporter
}

// Resolver returns a Resolver for s that logs lookup failures to l.
func (s *Spec) Resolver(l *log.Logger) *Resolver {
	return &Resolver{spec: s, lookup: lookupIPv4, errs: report.New(l)}
}

// Resolve returns the destinations for the current network configuration.
// A host name that doesn't resolve is left out until it does. A lookup
// cut short by ctx is not logged: that is a shutdown, not a failure.
func (r *Resolver) Resolve(ctx context.Context) List {
	s := r.spec
	out := slices.Clone(s.fixed)

	for _, h := range s.hosts {
		a, err := r.resolve(ctx, h)
		if ctx.Err() == nil {
			r.errs.Report("targets: "+h.String(), err)
		}

		if err == nil {
			out = append(out, a)
		}
	}

	if s.auto {
		out = append(out, netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), s.port))

		for _, ip := range localBroadcasts() {
			out = append(out, netip.AddrPortFrom(ip, s.port))
		}
	}

	return dedupe(out)
}

func dedupe(l List) List {
	seen := map[netip.AddrPort]bool{}

	return slices.DeleteFunc(l, func(a netip.AddrPort) bool {
		if seen[a] {
			return true
		}

		seen[a] = true

		return false
	})
}

// localBroadcasts returns the subnet broadcast address of every active
// IPv4 network. Subnet broadcasts are used instead of 255.255.255.255,
// which Windows sends out of one adapter only: with VPN or Hyper-V
// adapters, often not the Wi-Fi.
func localBroadcasts() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var addrs []net.Addr

	for i := range ifaces {
		const want = net.FlagUp | net.FlagRunning | net.FlagBroadcast
		if ifaces[i].Flags&want != want || ifaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}

		a, err := ifaces[i].Addrs()
		if err == nil {
			addrs = append(addrs, a...)
		}
	}

	return broadcasts(addrs)
}

// broadcasts returns the directed broadcast addresses of the IPv4 networks
// in addrs, skipping link-local networks and those too small to have one
// (/31 and /32).
func broadcasts(addrs []net.Addr) []netip.Addr {
	const maxBits = 30

	var out []netip.Addr

	for _, a := range addrs {
		p, ok := ipv4Prefix(a)
		if !ok || p.Bits() > maxBits || p.Addr().IsLinkLocalUnicast() || p.Addr().IsLoopback() {
			continue
		}

		b := p.Addr().As4()
		binary.BigEndian.PutUint32(b[:], binary.BigEndian.Uint32(b[:])|^uint32(0)>>p.Bits())
		out = append(out, netip.AddrFrom4(b))
	}

	return out
}

// ipv4Prefix returns the IPv4 network of an interface address. Windows
// reports IPv4 networks with a 4-byte mask, other systems may use 16
// bytes, whose size then counts the 96 leading IPv6 bits too.
func ipv4Prefix(a net.Addr) (netip.Prefix, bool) {
	const ipv4Bits, ipv6Bits = 32, 128

	n, ok := a.(*net.IPNet)
	if !ok {
		return netip.Prefix{}, false
	}

	ip, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}

	ones, bits := n.Mask.Size()
	if bits == ipv6Bits {
		ones, bits = ones-(ipv6Bits-ipv4Bits), ipv4Bits
	}

	ip = ip.Unmap()
	if !ip.Is4() || bits != ipv4Bits || ones < 0 {
		return netip.Prefix{}, false
	}

	return netip.PrefixFrom(ip, ones), true
}

// Describe explains a spec for the log, e.g. "auto (this PC + local
// networks) on port 49002".
func (s *Spec) Describe() string {
	parts := make([]string, 0, len(s.fixed)+len(s.hosts)+1)
	if s.auto {
		parts = append(parts, "auto (this PC + local networks, port "+
			strconv.Itoa(int(s.port))+")")
	}

	for _, a := range s.fixed {
		parts = append(parts, a.String())
	}

	for _, h := range s.hosts {
		parts = append(parts, h.String())
	}

	return strings.Join(parts, ", ")
}

// add adds one host:port target.
func (s *Spec) add(target string) error {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errSyntax
	}

	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return errNoPort
	}

	if host == "" {
		return errNoHost
	}

	ip, isIP := parseIP(host)
	if !isIP && !isHostName(host) {
		return errBadHost
	}

	if !isIP {
		s.hosts = append(s.hosts, hostPort{name: host, port: uint16(port)})

		return nil
	}

	if ip = ip.Unmap(); !ip.Is4() {
		return errNotIPv4
	}

	s.fixed = append(s.fixed, netip.AddrPortFrom(ip, uint16(port)))

	return nil
}

// isHostName reports whether host is a syntactically valid DNS name that
// can't be taken for a mistyped IPv4 address, such as 192.168.1.300: its
// labels are letters, digits and inner hyphens, and the last one is not all
// digits.
func isHostName(host string) bool {
	const maxLen = 253

	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(host) > maxLen || allDigits(labels[len(labels)-1]) {
		return false
	}

	return !slices.ContainsFunc(labels, func(l string) bool { return !isLabel(l) })
}

// isLabel reports whether l is a valid DNS label.
func isLabel(l string) bool {
	const maxLabel = 63

	if l == "" || len(l) > maxLabel || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}

	return !strings.ContainsFunc(l, func(r rune) bool { return !isLabelRune(r) })
}

func isLabelRune(r rune) bool {
	return r == '-' || '0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
}

func allDigits(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// parseIP parses an IP address literal. Anything else is a host name, not
// an error, which is why this reports a bool.
func parseIP(host string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(host)

	return ip, err == nil
}

// resolve returns the first IPv4 address of a host name target.
func (r *Resolver) resolve(ctx context.Context, h hostPort) (netip.AddrPort, error) {
	addrs, err := r.lookup(ctx, h.name)
	if err != nil {
		return netip.AddrPort{}, err
	}

	for _, a := range addrs {
		if a = a.Unmap(); a.Is4() {
			return netip.AddrPortFrom(a, h.port), nil
		}
	}

	return netip.AddrPort{}, errNotIPv4
}

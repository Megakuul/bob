// Package netguard keeps the server's outgoing requests to configured URLs (forge APIs,
// notification webhooks) away from internal addresses (I-R2 F8, decision A396): loopback,
// private, link-local (the cloud metadata service), shared, unspecified, multicast and reserved
// addresses are refused at connect time, after name resolution, unless the operator allows the
// host or range (server.outbound_allow, BOB_SERVER_OUTBOUND_ALLOW).
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ErrRefused is a connection the policy does not allow.
var ErrRefused = errors.New("refusing to connect to an internal address")

// Policy decides which addresses outgoing requests may reach. The zero value (and nil) allows
// public addresses only.
type Policy struct {
	hosts map[string]bool // host names allowed whatever they resolve to
	nets  []netip.Prefix  // addresses allowed although internal
}

// New returns the policy that additionally allows the given hosts: host names, IP addresses or
// CIDR ranges (e.g. "forge.corp.example", "10.1.2.3", "10.0.0.0/8").
func New(allow []string) (*Policy, error) {
	p := &Policy{hosts: map[string]bool{}}
	for _, a := range allow {
		a = strings.TrimSpace(a)
		switch {
		case a == "":
		case strings.Contains(a, "/"):
			pr, err := netip.ParsePrefix(a)
			if err != nil {
				return nil, fmt.Errorf("outbound allow entry %q: %w", a, err)
			}
			p.nets = append(p.nets, pr.Masked())
		default:
			if ip, err := netip.ParseAddr(a); err == nil {
				p.nets = append(p.nets, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
				continue
			}
			if strings.ContainsAny(a, " :@[]") {
				return nil, fmt.Errorf("outbound allow entry %q is neither a host name, an IP address nor a CIDR range", a)
			}
			p.hosts[strings.ToLower(strings.TrimSuffix(a, "."))] = true
		}
	}
	return p, nil
}

var refusedNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // shared address space (carrier-grade NAT)
		"192.0.0.0/24",    // IETF protocol assignments
		"198.18.0.0/15",   // benchmarking
		"240.0.0.0/4",     // reserved, broadcast
		"fec0::/10",       // site-local (deprecated)
		"64:ff9b:1::/48",  // local-use NAT64
		"2001:db8::/32",   // documentation
		"192.0.2.0/24",    // documentation
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// canonical is the IPv4 address an IPv4-mapped or NAT64 (64:ff9b::/96) address reaches, else ip.
func canonical(ip netip.Addr) netip.Addr {
	ip = ip.Unmap()
	if nat64.Contains(ip) {
		b := ip.As16()
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	return ip
}

// internal reports whether ip is an address an outgoing request must not reach by default.
func internal(ip netip.Addr) bool {
	ip = canonical(ip)
	if ip.IsLoopback() {
		return !testLoopback()
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || !ip.IsValid() {
		return true
	}
	for _, n := range refusedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Allowed reports whether a connection to ip, reached through host name host, is allowed.
func (p *Policy) Allowed(host string, ip netip.Addr) bool {
	if p != nil && p.hosts[strings.ToLower(strings.TrimSuffix(host, "."))] {
		return true
	}
	ip = canonical(ip)
	if p != nil {
		for _, n := range p.nets {
			if n.Contains(ip) {
				return true
			}
		}
	}
	return !internal(ip)
}

// DialContext dials addr ("host:port") through d, but only to addresses the policy allows: the
// host is resolved first and only its allowed addresses are dialed (so a name that resolves to
// an internal address, or that changes between check and connect, is refused).
func (p *Policy) DialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var ips []netip.Addr
		if ip, err := netip.ParseAddr(host); err == nil {
			ips = []netip.Addr{ip}
		} else if ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host); err != nil {
			return nil, err
		}
		var errs []error
		for _, ip := range ips {
			if !p.Allowed(host, ip) {
				errs = append(errs, fmt.Errorf("%w: %s is %s (allow it with server.outbound_allow)", ErrRefused, host, ip))
				continue
			}
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			errs = append(errs, err)
		}
		if len(errs) == 0 {
			return nil, fmt.Errorf("%s: no address", host)
		}
		return nil, errors.Join(errs...)
	}
}

// Client returns a copy of base whose connections go through the policy (base's TLS settings,
// timeouts and redirect rule are kept; nil base: a client with a 30 s timeout). A proxy from
// the environment (HTTPS_PROXY) is still used; the connection to it is checked like any other,
// so an internal proxy works only when the operator allows its address.
func (p *Policy) Client(base *http.Client) *http.Client {
	c := &http.Client{Timeout: 30 * time.Second}
	if base != nil {
		cc := *base
		c = &cc
	}
	var t *http.Transport
	if bt, ok := c.Transport.(*http.Transport); ok && bt != nil {
		t = bt.Clone()
	} else if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t = dt.Clone()
	} else {
		t = &http.Transport{}
	}
	t.DialContext = p.DialContext(&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})
	t.DialTLSContext = nil
	c.Transport = t
	return c
}

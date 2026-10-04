// Package netguard refuses outbound connections to non-public addresses.
//
// Control is installed as a net.Dialer's Control hook, so it runs on the
// address actually dialed, after DNS resolution and for every connection: a
// hostname cannot resolve, or later rebind, to an internal address and get
// through. Callers must not route through a proxy, whose address would be
// checked instead of the target's.
package netguard

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"syscall"
)

// ErrRefused reports a destination address that is not public.
var ErrRefused = errors.New("destination address is not allowed")

// RefusedError is the dial-time refusal. Address is the refused IP (or the
// raw dial address when it could not be parsed); it belongs in operator logs
// only, never in public error text.
type RefusedError struct{ Address string }

func (e *RefusedError) Error() string { return ErrRefused.Error() }
func (e *RefusedError) Unwrap() error { return ErrRefused }

// Allowed reports whether ip is a public unicast address. IPv4-mapped IPv6
// addresses are judged as the IPv4 address they carry.
func Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Control is a net.Dialer Control hook that refuses non-public addresses.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return &RefusedError{Address: address}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !Allowed(ip) {
		return &RefusedError{Address: host}
	}
	return nil
}

// CheckLiteral refuses a configured host that is plainly not public: a
// literal non-public IP or a localhost name. Other hostnames pass; only the
// dial-time Control can judge what they resolve to.
func CheckLiteral(host string) error {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return &RefusedError{Address: host}
	}
	if ip, err := netip.ParseAddr(h); err == nil && !Allowed(ip) {
		return &RefusedError{Address: host}
	}
	return nil
}

// nonPublic lists special-purpose ranges that IsGlobalUnicast accepts,
// including IPv6 prefixes that embed an IPv4 target.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("::/96"),           // IPv4-compatible (deprecated)
	netip.MustParsePrefix("::ffff:0:0:0/96"), // IPv4-translated
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local NAT64
	netip.MustParsePrefix("100::/64"),        // discard
	netip.MustParsePrefix("2001::/32"),       // Teredo
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4
	netip.MustParsePrefix("fec0::/10"),       // site-local (deprecated)
}

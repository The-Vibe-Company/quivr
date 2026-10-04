package main

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
)

// A copy of the core's internal/netguard rule, which a plugin cannot import.
// refusePrivate is a net.Dialer Control hook: it runs on the address actually
// dialed, after DNS resolution and for every connection, so a hostname cannot
// resolve, or later rebind, to an internal address and get through. The
// transport never uses a proxy, whose address would be checked instead.

var errAddressRefused = errors.New("destination address is not allowed")

func refusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errAddressRefused
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !public(ip) {
		return errAddressRefused
	}
	return nil
}

// public reports whether ip is a public unicast address. IPv4-mapped IPv6
// addresses are judged as the IPv4 address they carry.
func public(ip netip.Addr) bool {
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

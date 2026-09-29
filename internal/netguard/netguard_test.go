package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefusesEveryNonPublicRange(t *testing.T) {
	refused := map[string][]string{
		"unspecified":        {"0.0.0.0", "::"},
		"this network":       {"0.1.2.3"},
		"loopback":           {"127.0.0.1", "127.255.255.254", "::1"},
		"private rfc1918":    {"10.0.0.1", "172.16.0.1", "172.31.255.254", "192.168.1.1"},
		"ula":                {"fc00::1", "fd12:3456::1"},
		"link-local":         {"169.254.1.1", "fe80::1"},
		"cloud metadata":     {"169.254.169.254", "fd00:ec2::254"},
		"carrier-grade nat":  {"100.64.0.1", "100.127.255.254"},
		"multicast":          {"224.0.0.1", "239.255.255.250", "ff02::1", "ff05::2"},
		"broadcast":          {"255.255.255.255"},
		"ietf protocol":      {"192.0.0.8"},
		"documentation":      {"192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1"},
		"6to4 relay anycast": {"192.88.99.1"},
		"benchmarking":       {"198.18.0.1", "198.19.255.254"},
		"reserved":           {"240.0.0.1"},
		"ipv4-mapped":        {"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1"},
		"ipv4-compatible":    {"::127.0.0.1", "::a9fe:a9fe"},
		"ipv4-translated":    {"::ffff:0:7f00:1"},
		"nat64":              {"64:ff9b::a9fe:a9fe", "64:ff9b::7f00:1", "64:ff9b:1::1"},
		"6to4":               {"2002:a9fe:a9fe::1", "2002:7f00:1::1"},
		"teredo":             {"2001:0:4136:e378::1"},
		"discard":            {"100::1"},
		"site-local":         {"fec0::1"},
	}
	for kind, addrs := range refused {
		for _, a := range addrs {
			ip := netip.MustParseAddr(a)
			if Allowed(ip) {
				t.Errorf("%s %s allowed", kind, a)
			}
			err := Control("tcp", net.JoinHostPort(a, "443"), nil)
			if !errors.Is(err, ErrRefused) {
				t.Errorf("%s %s dial not refused: %v", kind, a, err)
			}
		}
	}
	for _, a := range []string{"93.184.216.34", "1.1.1.1", "8.8.8.8", "2606:2800:220:1::1", "2a00:1450:4007::1", "::ffff:93.184.216.34"} {
		if !Allowed(netip.MustParseAddr(a)) {
			t.Errorf("public %s refused", a)
		}
		if err := Control("tcp", net.JoinHostPort(a, "443"), nil); err != nil {
			t.Errorf("public %s dial refused: %v", a, err)
		}
	}
}

func TestControlRefusesMalformedAddresses(t *testing.T) {
	for _, a := range []string{"", "no-port", "example.org:443", "[::1", "1.2.3:80"} {
		if err := Control("tcp", a, nil); !errors.Is(err, ErrRefused) {
			t.Errorf("%q not refused: %v", a, err)
		}
	}
}

func TestRefusedErrorCarriesTheAddressForOperatorsOnly(t *testing.T) {
	err := Control("tcp", "169.254.169.254:80", nil)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Address != "169.254.169.254" {
		t.Fatalf("typed refusal: %#v", err)
	}
}

func TestCheckLiteral(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.1", "localhost", "LOCALHOST", "api.localhost", "localhost.", "::ffff:127.0.0.1"} {
		if err := CheckLiteral(host); !errors.Is(err, ErrRefused) {
			t.Errorf("%s accepted", host)
		}
	}
	for _, host := range []string{"93.184.216.34", "2606:2800:220:1::1", "hooks.example.org", "example.org"} {
		if err := CheckLiteral(host); err != nil {
			t.Errorf("%s refused: %v", host, err)
		}
	}
}

// A hostname is checked on the address it resolves to at dial time: a name
// that resolves to loopback never reaches the server.
func TestDialerRefusesANameResolvingToLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	dialer := &net.Dialer{Timeout: 2 * time.Second, Control: Control}
	client := &http.Client{Transport: &http.Transport{DialContext: dialer.DialContext}}
	for _, target := range []string{srv.URL, "http://localhost:" + port} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader("{}"))
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s reached", target)
		}
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v", target, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests reached a loopback server", hits.Load())
	}
}

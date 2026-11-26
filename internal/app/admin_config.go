package app

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

func (cfg Config) validateAdminListener() (separated bool, shareProbe bool, err error) {
	if cfg.AdminListen == "" {
		if len(cfg.AdminKeys) > 0 {
			return false, false, badConfig(configConflict, "admin_keys", "admin_keys requires admin_listen")
		}
		return false, false, nil
	}
	adminHost, adminPort, ok := parseListenAddress(cfg.AdminListen)
	if !ok {
		return false, false, badConfig(configInvalid, "admin_listen", "admin_listen must be a valid host:port address")
	}
	listenHost, listenPort, listenOK := parseListenAddress(cfg.Listen)
	if listenOK && listenAddressesCollide(listenHost, listenPort, adminHost, adminPort) {
		return false, false, badConfig(configConflict, "admin_listen", "admin_listen must differ from listen")
	}
	probeHost, probePort, probeOK := parseListenAddress(cfg.ProbeListen)
	if probeOK && sameListenAddress(probeHost, probePort, adminHost, adminPort) {
		shareProbe = true
	} else if probeOK && listenAddressesCollide(probeHost, probePort, adminHost, adminPort) {
		return false, false, badConfig(configConflict, "admin_listen", "admin_listen conflicts with probe_listen")
	}
	if len(cfg.AdminKeys) == 0 {
		return false, false, badConfig(configMissing, "admin_keys", "admin_keys must contain at least one scoped admin key when admin_listen is configured")
	}
	for token, s := range cfg.AdminKeys {
		if len(token) < 32 || s.Organization == "" || len(s.Actions) == 0 || len(s.Corpora) == 0 {
			return false, false, badConfig(configInvalid, "admin_keys", "invalid scoped admin credential configuration")
		}
		if _, overlap := cfg.Keys[token]; overlap {
			return false, false, badConfig(configConflict, "admin_keys", "admin_keys must not reuse functional API keys")
		}
	}
	return true, shareProbe, nil
}

func parseListenAddress(addr string) (string, int, bool) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil || portText == "" {
		return "", 0, false
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return "", 0, false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return "", 0, false
	}
	return normalizeListenHost(host), port, true
}

func normalizeListenHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.IsUnspecified() {
			return ""
		}
		return ip.String()
	}
	return strings.ToLower(host)
}

func sameListenAddress(hostA string, portA int, hostB string, portB int) bool {
	return portA != 0 && portA == portB && hostA == hostB
}

func listenAddressesCollide(hostA string, portA int, hostB string, portB int) bool {
	if portA == 0 || portA != portB {
		return false
	}
	return hostA == hostB || hostA == "" || hostB == ""
}

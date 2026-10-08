//go:build linux

package devhost

import (
	"io"
	"net"
	"time"
)

// reserve leaves the listener's port in TIME_WAIT for 60 seconds: it connects
// to itself, and the accepted side closes first. Until then the kernel hands
// the port to no other listen on port 0 and no outgoing connection, while a
// listener with SO_REUSEADDR (Go's net.Listen, Python's http.server and most
// servers on Unix) can still bind it. That closes the race between choosing
// the plugin's port and the plugin binding it. On failure the port is only
// unreserved, as on other systems.
func reserve(l net.Listener) {
	// Bounds a loopback handshake, never a wait for anything else.
	deadline := time.Now().Add(time.Second)
	if tl, ok := l.(*net.TCPListener); ok {
		_ = tl.SetDeadline(deadline)
	}
	client, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
	if err != nil {
		return
	}
	defer client.Close()
	_ = client.SetDeadline(deadline)
	server, err := l.Accept()
	if err != nil {
		return
	}
	_ = server.Close()
	// Another process may have connected first; its connection says nothing
	// about ours.
	if server.RemoteAddr().String() != client.LocalAddr().String() {
		return
	}
	// EOF means the accepted side has sent its FIN, so it is the side that
	// enters TIME_WAIT once client closes.
	_, _ = io.Copy(io.Discard, client)
}

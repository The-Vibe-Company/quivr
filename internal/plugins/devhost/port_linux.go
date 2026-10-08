//go:build linux

package devhost

import (
	"io"
	"net"
)

// reserve leaves the listener's port in TIME_WAIT for 60 seconds: it connects
// to itself, and the accepted side closes first. Until then the kernel hands
// the port to no other listen on port 0 and no outgoing connection, while a
// listener with SO_REUSEADDR (Go's net.Listen, Python's http.server and most
// servers on Unix) can still bind it. That closes the race between choosing
// the plugin's port and the plugin binding it. On failure the port is only
// unreserved, as on other systems.
func reserve(l net.Listener) {
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		return
	}
	defer client.Close()
	server, err := l.Accept()
	if err != nil {
		return
	}
	_ = server.Close()
	// EOF means the accepted side has sent its FIN, so it is the side that
	// enters TIME_WAIT once client closes.
	_, _ = io.Copy(io.Discard, client)
}

//go:build !linux

package devhost

import "net"

// reserve does nothing outside Linux: the port is only probed as free.
func reserve(net.Listener) {}

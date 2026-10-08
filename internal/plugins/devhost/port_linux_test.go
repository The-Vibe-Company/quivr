package devhost_test

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost/fakeplugin"
)

// TestAssignedPortStaysReservedForThePlugin owns the port handover between
// FreePort and the plugin's own listener (THE-1059). It forces the collision a
// concurrent process causes: another socket binds the assigned port before the
// plugin starts. The kernel's own port choices (listen on port 0, outgoing
// connections) skip every port such a plain bind is refused, so the refusal
// shows no concurrent process can be handed the port. The plugin binds with
// SO_REUSEADDR, like the SDKs, and must still become healthy.
func TestAssignedPortStaysReservedForThePlugin(t *testing.T) {
	dir, _ := writePlugin(t)
	port, err := devhost.FreePort("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	plain := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var err error
		if cerr := c.Control(func(fd uintptr) { err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 0) }); cerr != nil {
			return cerr
		}
		return err
	}}
	if occupant, err := plain.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
		defer occupant.Close()
		t.Errorf("another socket took port %d between FreePort and the plugin's start", port)
	} else if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("occupy port %d: %v", port, err)
	}
	p, err := devhost.Start(devhost.Options{Dir: dir, Command: fakeplugin.Command(), Manifest: filepath.Join(dir, plugins.ManifestFile),
		Port: port, Env: []string{fakeplugin.EnvEnable + "=1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.WaitHealthy(ctx); err != nil {
		t.Fatalf("plugin on port %d not healthy: %v", port, err)
	}
}

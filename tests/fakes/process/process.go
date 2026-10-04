// Package process runs the shared API fake binaries for Go test suites.
package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type Server struct{ URL string }

type build struct {
	once sync.Once
	path string
	err  error
}

var builds sync.Map

func binary(name string) (string, error) {
	v, _ := builds.LoadOrStore(name, &build{})
	b := v.(*build)
	b.once.Do(func() {
		root, err := os.Getwd()
		if err != nil {
			b.err = err
			return
		}
		for {
			if _, err := os.Stat(filepath.Join(root, "tests", "fakes", "go.mod")); err == nil {
				break
			}
			parent := filepath.Dir(root)
			if parent == root {
				b.err = fmt.Errorf("cannot find tests/fakes from working directory")
				return
			}
			root = parent
		}
		work := filepath.Join(root, ".scratch", "fakes")
		if err := os.MkdirAll(work, 0755); err != nil {
			b.err = err
			return
		}
		out, err := os.CreateTemp(work, name+"-*")
		if err != nil {
			b.err = err
			return
		}
		out.Close()
		defer os.Remove(out.Name())
		goCmd := os.Getenv("GO")
		if goCmd == "" {
			goCmd = "go"
		}
		cmd := exec.Command(goCmd, "build", "-o", out.Name(), "./cmd/"+name)
		cmd.Dir = filepath.Join(root, "tests", "fakes")
		if log, err := cmd.CombinedOutput(); err != nil {
			b.err = fmt.Errorf("build %s: %w\n%s", name, err, log)
			return
		}
		b.path = filepath.Join(work, name)
		b.err = os.Rename(out.Name(), b.path)
	})
	return b.path, b.err
}

// Start builds once per suite, starts isolated state, and waits for the actual
// bound address on stdout. Cleanup kills and reaps only this test's process.
func Start(t testing.TB, name string, args ...string) *Server {
	t.Helper()
	path, err := binary(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := make(chan struct {
		server Server
		err    error
	}, 1)
	go func() {
		var s Server
		err := json.NewDecoder(stdout).Decode(&s)
		ready <- struct {
			server Server
			err    error
		}{s, err}
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case result := <-ready:
		if result.err != nil || result.server.URL == "" {
			cancel()
			<-done
			t.Fatalf("%s readiness: %v; %s", name, result.err, stderr.String())
		}
		t.Cleanup(func() { cancel(); <-done })
		return &result.server
	case <-timer.C:
		cancel()
		<-done
		t.Fatalf("%s did not report its bound address: %s", name, stderr.String())
	}
	return nil
}

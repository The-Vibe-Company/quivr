package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Observe the admission select after preflight validation, before canceling a
// queued caller. No production hook or scheduler delay is needed.
type tokenizerWaitContext struct {
	context.Context
	started chan struct{}
	once    sync.Once
}

func (c *tokenizerWaitContext) Done() <-chan struct{} {
	done := c.Context.Done()
	c.once.Do(func() { close(c.started) })
	return done
}

// Owns local helper concurrency and recovery. Existing packing tests own cuts;
// this fake replaces only the external executable, using request barriers.
func TestLocalTokenizerOverlapsAndRecovers(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	script := filepath.Join(dir, "python")
	source := fmt.Sprintf(`#!%s
import json,os,socket,sys
s=socket.socket(socket.AF_UNIX)
s.connect(sys.argv[4])
s.sendall((json.dumps({'pid':os.getpid(),'text':'startup'})+'\n').encode())
s.recv(1)
s.close()
print('ready',flush=True)
for line in sys.stdin:
    rows=json.loads(line)
    s=socket.socket(socket.AF_UNIX)
    s.connect(sys.argv[4])
    s.sendall((json.dumps({'pid':os.getpid(),'text':rows[0]['text']})+'\n').encode())
    action=s.recv(1)
    s.close()
    if action==b'x': sys.exit(1)
    print(json.dumps([{'tokens':3,'offsets':[[0,1],[1,2],[2,3]]} for row in rows]),flush=True)
`, python)
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	c := testConfig("openai", "http://127.0.0.1:9")
	c.Tokenizer = &tokenizerConfiguration{Python: script, Model: socket, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	c.TokenizerProcesses = 2
	i := newIngester(c, "", slog.New(slog.DiscardHandler))
	if closer, ok := i.tokenizer.(interface{ Close() }); ok {
		t.Cleanup(closer.Close)
	}
	type request struct {
		net.Conn
		Text string
		PID  int
	}
	entered := make(chan request, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var payload struct {
				Text string `json:"text"`
				PID  int    `json:"pid"`
			}
			if json.NewDecoder(conn).Decode(&payload) != nil {
				_ = conn.Close()
				continue
			}
			entered <- request{conn, payload.Text, payload.PID}
		}
	}()
	event := func() request {
		t.Helper()
		select {
		case r := <-entered:
			t.Cleanup(func() { _ = r.Close() })
			return r
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent tokenizer request did not enter before held exchanges completed")
			return request{}
		}
	}
	next := func() request {
		t.Helper()
		for {
			r := event()
			if r.Text != "startup" {
				return r
			}
			if _, err := r.Write([]byte("r")); err != nil {
				t.Fatal(err)
			}
		}
	}
	type result struct {
		out []tokenEncoding
		err error
	}
	start := func(ctx context.Context, text string) <-chan result {
		done := make(chan result, 1)
		go func() { out, err := i.tokenizer.Encode(ctx, []tokenInput{{Text: text}}); done <- result{out, err} }()
		return done
	}
	finish := func(done <-chan result, success bool) {
		t.Helper()
		select {
		case r := <-done:
			if success {
				if r.err != nil || len(r.out) != 1 || r.out[0].Tokens != 3 || len(r.out[0].Offsets) != 3 {
					t.Fatalf("encoding = %+v, error %v", r.out, r.err)
				}
			} else if r.err == nil {
				t.Fatal("failed helper unexpectedly succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("tokenizer call failed to complete")
		}
	}
	release := func(r request, action string) {
		t.Helper()
		if _, err := r.Write([]byte(action)); err != nil {
			t.Fatal(err)
		}
	}
	// Startup is held beyond a caller's deadline. Cancellation must leave
	// both helpers alive so subsequent calls use those exact processes.
	startupA, startupB := event(), event()
	coldCtx, cancelCold := context.WithCancel(t.Context())
	defer cancelCold()
	waitingCold := &tokenizerWaitContext{Context: coldCtx, started: make(chan struct{})}
	cold := start(waitingCold, "cold")
	select {
	case <-waitingCold.started:
	case <-time.After(3 * time.Second):
		t.Fatal("cold caller did not reach admission")
	}
	cancelCold()
	finish(cold, false)
	release(startupA, "r")
	release(startupB, "r")
	first := start(t.Context(), "one")
	a := next()
	second := start(t.Context(), "two")
	b := next()
	if (a.PID != startupA.PID && a.PID != startupB.PID) || (b.PID != startupA.PID && b.PID != startupB.PID) {
		t.Fatal("startup cancellation replaced a healthy helper")
	}
	if a.PID == b.PID {
		t.Fatal("concurrent exchanges shared a process")
	}
	// Fill both slots and cancel a caller at the admission select.
	canceled, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := &tokenizerWaitContext{Context: canceled, started: make(chan struct{})}
	queued := start(waiting, "queued")
	select {
	case <-waiting.started:
	case <-time.After(3 * time.Second):
		t.Fatal("queued caller did not reach admission")
	}
	cancel()
	finish(queued, false)
	select {
	case r := <-entered:
		t.Fatalf("canceled queued request entered: %s", r.Text)
	default:
	}
	third := start(t.Context(), "three")
	release(a, "r")
	finish(first, true)
	d := next()
	if d.Text != "three" || d.PID != a.PID {
		t.Fatalf("next request did not reuse the available helper: %+v", d)
	}
	// Crash one helper while the other still has a request in progress.
	release(b, "x")
	finish(second, false)
	replacement := start(t.Context(), "replacement")
	e := next()
	if e.PID == b.PID || e.PID == a.PID {
		t.Fatal("crashed helper was not replaced independently")
	}
	release(e, "r")
	finish(replacement, true)
	release(d, "r")
	finish(third, true)
	// A cancelled exchange returns promptly while its helper drains the answer.
	heldCtx, cancelHeld := context.WithCancel(t.Context())
	defer cancelHeld()
	held := start(heldCtx, "held")
	h := next()
	healthy := start(t.Context(), "healthy")
	f := next()
	release(f, "r")
	finish(healthy, true)
	cancelHeld()
	finish(held, false)
	release(h, "r")
	// Hold the healthy process so the next call must use the recovered slot.
	occupy := start(t.Context(), "occupy")
	g := next()
	recovered := start(t.Context(), "recovered")
	j := next()
	if j.PID != h.PID && g.PID != h.PID {
		t.Fatal("caller cancellation replaced the healthy helper")
	}
	if j.PID == g.PID {
		t.Fatal("recovered request shared an occupied helper")
	}
	release(j, "r")
	finish(recovered, true)
	release(g, "r")
	finish(occupy, true)
}

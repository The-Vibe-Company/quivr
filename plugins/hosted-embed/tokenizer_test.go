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
	"testing"
	"time"
)

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
	next := func() request {
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
	first := start(t.Context(), "one")
	a := next()
	second := start(t.Context(), "two")
	b := next()
	if a.PID == b.PID {
		t.Fatal("concurrent exchanges shared a process")
	}
	// Fill both slots; an already-canceled caller must not reach the helper.
	canceled, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := &queryWaitContext{Context: canceled, started: make(chan struct{})}
	queued := start(waiting, "queued")
	<-waiting.started
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
	// A hung exchange cancels promptly; healthy work completes independently.
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
	// Hold the healthy process so the next call must use the recovered slot.
	occupy := start(t.Context(), "occupy")
	g := next()
	recovered := start(t.Context(), "recovered")
	j := next()
	if j.PID == h.PID {
		t.Fatal("canceled hung process was reused")
	}
	release(j, "r")
	finish(recovered, true)
	release(g, "r")
	finish(occupy, true)
}

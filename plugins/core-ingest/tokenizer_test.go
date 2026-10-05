package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer speaks the persistent protocol without the pinned tokenizer: each
// response reports the text length as its token count. Directives embedded in the
// text drive failure scenarios.
const fakeServer = `
import json, socket, sys
sys.stdout.write('ready\n'); sys.stdout.flush()
for line in sys.stdin:
    request = json.loads(line)
    text = request[0]['text'] if request else ''
    if text == 'crash': sys.exit(3)
    if text == 'hang': sys.stdin.readline()
    if text == 'block':
        host, port = sys.argv[1].rsplit(':', 1)
        with socket.create_connection((host, int(port))) as gate:
            gate.sendall(b'R')
            gate.recv(1)
    if text == 'reject': sys.stdout.write('null\n'); sys.stdout.flush(); continue
    if text == 'huge': sys.stdout.write('[' + '0' * 4096 + ']\n'); sys.stdout.flush(); continue
    if text == 'short': sys.stdout.write('[]\n'); sys.stdout.flush(); continue
    sys.stdout.write(json.dumps([dict(tokens=len(x['text']), offsets=[[0, len(x['text'])]]) for x in request]) + '\n'); sys.stdout.flush()
`

func fake(t *testing.T, source string) *Server {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	s := &Server{Config: TokenizerConfig{Python: python, Model: "unused"}, source: source, timeout: 2 * time.Second, maxResponse: 1024}
	t.Cleanup(s.Close)
	return s
}

func encodeText(s *Server, ctx context.Context, text string) ([]Encoding, error) {
	return s.Encode(ctx, []TokenInput{{Text: text}})
}

func TestServerRoundTripsBatchesOnOneProcess(t *testing.T) {
	s := fake(t, fakeServer)
	for i := range 5 {
		text := fmt.Sprintf("query %d\nwith newline", i)
		got, err := s.Encode(context.Background(), []TokenInput{{Text: text}, {Text: "query: " + text, Special: true}})
		if err != nil {
			t.Fatal(err)
		}
		want := []Encoding{{Tokens: len(text), Offsets: [][2]int{{0, len(text)}}}, {Tokens: len(text) + 7, Offsets: [][2]int{{0, len(text) + 7}}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("unexpected encodings", got)
		}
	}
	if s.spawns != 1 {
		t.Fatal("expected one long-lived process, spawned", s.spawns)
	}
}

func TestServerRejectedRequestKeepsProcess(t *testing.T) {
	s := fake(t, fakeServer)
	if _, err := encodeText(s, context.Background(), "reject"); err == nil || err.Error() != "tokenizer unavailable" {
		t.Fatal("rejected request must fail closed", err)
	}
	if _, err := encodeText(s, context.Background(), "ok"); err != nil || s.spawns != 1 {
		t.Fatal("process should survive a rejected request", err, s.spawns)
	}
}

func TestServerRespawnsAfterFailures(t *testing.T) {
	for _, text := range []string{"crash", "hang", "huge", "short"} {
		t.Run(text, func(t *testing.T) {
			s := fake(t, fakeServer)
			if _, err := encodeText(s, context.Background(), "warm"); err != nil {
				t.Fatal(err)
			}
			if text == "hang" {
				// An already-expired timeout kills the blocked process without waiting.
				s.timeout = -1
			}
			process := s.proc.cmd.Process
			t.Cleanup(func() { _ = process.Kill() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := encodeText(s, ctx, text)
			if ctx.Err() != nil {
				t.Fatal("failure remained blocked until the caller watchdog")
			}
			if err == nil {
				t.Fatal("failure not reported")
			} else if text == "hang" && !errors.Is(err, errUnavailable) {
				t.Fatalf("hard timeout: got %v, want tokenizer unavailable", err)
			}
			s.timeout = 2 * time.Second
			if got, err := encodeText(s, context.Background(), "ok"); err != nil || got[0].Tokens != 2 || s.spawns != 2 {
				t.Fatal("expected a fresh process after failure", err, s.spawns)
			}
		})
	}
}

func TestServerCallerCancellationDoesNotKillProcess(t *testing.T) {
	s := fake(t, fakeServer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	s.Config.Model = listener.Addr().String()
	if _, err := encodeText(s, context.Background(), "warm"); err != nil {
		t.Fatal(err)
	}
	s.timeout = 30 * time.Second // watchdog; the socket gate controls completion
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := encodeText(s, ctx, "block")
		done <- err
	}()
	gate, err := listener.Accept()
	if err != nil {
		t.Fatal("tokenizer did not receive the request:", err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	if err := gate.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 1)
	if _, err := io.ReadFull(gate, ready); err != nil || ready[0] != 'R' {
		t.Fatalf("tokenizer did not block at the gate: %q, %v", ready, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errUnavailable) {
			t.Fatalf("cancelled call: got %v, want tokenizer unavailable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled caller stayed blocked behind the tokenizer")
	}
	if _, err := gate.Write([]byte{'G'}); err != nil {
		t.Fatal("tokenizer was killed before release:", err)
	}
	if _, err := encodeText(s, context.Background(), "ok"); err != nil || s.spawns != 1 {
		t.Fatal("cancellation should not respawn the tokenizer", err, s.spawns)
	}
}

func TestServerConcurrentCallersGetTheirOwnResults(t *testing.T) {
	s := fake(t, fakeServer)
	var wg sync.WaitGroup
	errs := make(chan error, 16*20)
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				text := fmt.Sprintf("%0*d", g+i+1, 0)
				got, err := encodeText(s, context.Background(), text)
				if err != nil || got[0].Tokens != len(text) {
					errs <- fmt.Errorf("caller %d/%d: %v %v", g, i, err, got)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if s.spawns != 1 {
		t.Fatal("spawned", s.spawns)
	}
}

func TestServerReportsStartupFailure(t *testing.T) {
	broken := fake(t, "import sys; sys.exit(1)")
	if _, err := encodeText(broken, context.Background(), "ok"); err == nil {
		t.Fatal("startup failure must be reported")
	}
	notReady := fake(t, "print('hello')")
	if _, err := encodeText(notReady, context.Background(), "ok"); err == nil {
		t.Fatal("missing ready handshake must be reported")
	}
}

func TestServerKeepsGoSideLimits(t *testing.T) {
	s := fake(t, fakeServer)
	var refusal *Refusal
	if _, err := s.Encode(context.Background(), make([]TokenInput, 513)); !errors.As(err, &refusal) || refusal.Code != "segmentation_limit" || s.spawns != 0 {
		t.Fatal("batch limit", err)
	}
}

func TestServerClosedIsUnavailable(t *testing.T) {
	s := fake(t, fakeServer)
	if _, err := encodeText(s, context.Background(), "ok"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := encodeText(s, context.Background(), "ok"); err == nil || s.spawns != 1 {
		t.Fatal("closed server must not respawn", err)
	}
}

func TestServerPinsTheProcessingProfile(t *testing.T) {
	var pinned struct {
		Implementation string `json:"implementation_version"`
		Tokenizer      string `json:"tokenizer_sha256"`
	}
	if err := json.Unmarshal(profile, &pinned); err != nil {
		t.Fatal(err)
	}
	for _, pin := range []string{"TOKENIZERS_VERSION = '" + pinned.Implementation + "'", "TOKENIZER_SHA256 = '" + pinned.Tokenizer + "'"} {
		if !strings.Contains(serverSource, pin) {
			t.Fatal("persistent tokenizer does not pin", pin)
		}
	}
}

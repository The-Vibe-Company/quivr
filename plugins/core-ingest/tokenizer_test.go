package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
import json, sys, time
sys.stdout.write('ready\n'); sys.stdout.flush()
for line in sys.stdin:
    request = json.loads(line)
    text = request[0]['text'] if request else ''
    if text == 'crash': sys.exit(3)
    if text.startswith('sleep:'): time.sleep(float(text[6:]))
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
	for _, text := range []string{"crash", "sleep:5", "huge", "short"} {
		t.Run(text, func(t *testing.T) {
			s := fake(t, fakeServer)
			s.timeout = 300 * time.Millisecond
			start := time.Now()
			if _, err := encodeText(s, context.Background(), text); err == nil {
				t.Fatal("failure not reported")
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("hard timeout not enforced")
			}
			if got, err := encodeText(s, context.Background(), "ok"); err != nil || got[0].Tokens != 2 || s.spawns != 2 {
				t.Fatal("expected a fresh process after failure", err, s.spawns)
			}
		})
	}
}

func TestServerCallerCancellationDoesNotKillProcess(t *testing.T) {
	s := fake(t, fakeServer)
	if _, err := encodeText(s, context.Background(), "warm"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := encodeText(s, ctx, "sleep:0.4"); err == nil {
		t.Fatal("cancelled call must fail")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("cancelled caller waited for the tokenizer")
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

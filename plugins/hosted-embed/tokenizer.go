package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const specialTokens = 8

type tokenizerConfiguration struct {
	Python string `json:"python"`
	Model  string `json:"model"`
	SHA256 string `json:"sha256"`
}
type tokenInput struct {
	Text    string `json:"text"`
	Special bool   `json:"special"`
}
type tokenEncoding struct {
	Tokens  int      `json:"tokens"`
	Offsets [][2]int `json:"offsets"`
}
type tokenCounter interface {
	Encode(context.Context, []tokenInput) ([]tokenEncoding, error)
}

// The conservative fallback remains available for providers with no local
// tokenizer. Each byte is an upper bound, never presented as an exact count.
type byteCounter struct{}

func (byteCounter) Encode(_ context.Context, inputs []tokenInput) ([]tokenEncoding, error) {
	out := make([]tokenEncoding, len(inputs))
	for i, in := range inputs {
		n := 0
		for _, r := range in.Text {
			for range utf8.RuneLen(r) {
				out[i].Offsets = append(out[i].Offsets, [2]int{n, n + 1})
			}
			n++
		}
		out[i].Tokens = len(out[i].Offsets)
		if in.Special {
			out[i].Tokens += specialTokens
		}
	}
	return out, nil
}

// A bounded pool keeps checksum-pinned helpers loaded across documents.
// A slot has exclusive ownership of its process until an exchange is reaped.
type localTokenizer struct {
	slots     chan *tokenizerProcess
	closed    chan struct{}
	closeOnce sync.Once
	ready     chan struct{}
	log       *slog.Logger
	helpers   atomic.Int64
	restarts  atomic.Int64
}

type tokenizerProcess struct {
	config  tokenizerConfiguration
	pool    *localTokenizer
	started bool
	cmd     *exec.Cmd
	writer  *bufio.Writer
	reader  *bufio.Reader
}

func newLocalTokenizer(config tokenizerConfiguration, processes int, log *slog.Logger) *localTokenizer {
	if processes == 0 {
		processes = min(runtime.GOMAXPROCS(0), 4)
	}
	t := &localTokenizer{slots: make(chan *tokenizerProcess, processes), closed: make(chan struct{}), ready: make(chan struct{}), log: log}
	var starting sync.WaitGroup
	for range processes {
		starting.Go(func() {
			p := &tokenizerProcess{config: config, pool: t}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := p.exchange(ctx, nil, 0); err != nil {
				t.log.Warn("tokenizer helper startup failed", "helper_count", t.helpers.Load(), "restarts", t.restarts.Load())
			}
			t.slots <- p
		})
	}
	go func() { starting.Wait(); close(t.ready) }()
	return t
}

// Close stops admission and reaps every helper after its current exchange ends.
func (t *localTokenizer) Close() {
	t.closeOnce.Do(func() {
		close(t.closed)
		for range cap(t.slots) {
			(<-t.slots).stop()
		}
	})
}

const tokenizerSource = `
import hashlib,json,sys
import tokenizers
from tokenizers import Tokenizer
if tokenizers.__version__!='0.23.2': raise ValueError('tokenizer version')
data=open(sys.argv[1],'rb').read()
if hashlib.sha256(data).hexdigest()!=sys.argv[2]: raise ValueError('tokenizer checksum')
t=Tokenizer.from_str(data.decode('utf-8'));t.no_truncation();t.no_padding()
print('ready',flush=True)
for line in sys.stdin.buffer:
    try:
        rows=json.loads(line)
        if len(line)>4*1024*1024 or len(rows)>512: raise ValueError('tokenizer limit')
        result=[]
        for row in rows:
            e=t.encode(row['text'],add_special_tokens=row['special'])
            result.append({'tokens':len(e.ids),'offsets':e.offsets})
        print(json.dumps(result),flush=True)
    except Exception:
        print('null',flush=True)
`

var errTokenizer = errors.New("local model tokenizer unavailable")

func (p *tokenizerProcess) stop() {
	if p.cmd != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		p.cmd = nil
		p.writer, p.reader = nil, nil
		p.pool.log.Info("tokenizer helper stopped", "helper_count", p.pool.helpers.Add(-1), "restarts", p.pool.restarts.Load())
	}
}

func (p *tokenizerProcess) exchange(ctx context.Context, raw []byte, count int) (out []tokenEncoding, err error) {
	fresh := p.cmd == nil
	if fresh {
		cmd := exec.Command(p.config.Python, "-u", "-c", tokenizerSource, p.config.Model, p.config.SHA256)
		stdin, pipeErr := cmd.StdinPipe()
		if pipeErr != nil {
			return nil, errTokenizer
		}
		stdout, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			_ = stdin.Close()
			return nil, errTokenizer
		}
		if cmd.Start() != nil {
			_ = stdin.Close()
			_ = stdout.Close()
			return nil, errTokenizer
		}
		p.cmd = cmd
		if p.started {
			p.pool.restarts.Add(1)
		}
		p.started = true
		p.pool.log.Info("tokenizer helper started", "helper_count", p.pool.helpers.Add(1), "restarts", p.pool.restarts.Load())
		p.writer = bufio.NewWriter(stdin)
		p.reader = bufio.NewReader(stdout)
	}
	// Only the independent hard deadline may kill this process.
	// Capture this process only: cancellation cannot kill another slot or a
	// replacement. Join an already-running callback before returning the lease.
	cmd := p.cmd
	killed := make(chan struct{})
	stopKill := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill(); close(killed) })
	defer func() {
		if !stopKill() {
			<-killed
		}
		if ctx.Err() != nil {
			out, err = nil, errTokenizer
		}
		if err != nil {
			p.stop()
		}
	}()
	if fresh {
		// ReadSlice limits the readiness line to the reader's fixed buffer.
		ready, readErr := p.reader.ReadSlice('\n')
		if readErr != nil || string(ready) != "ready\n" {
			return nil, errTokenizer
		}
	}
	// A nil request prestarts the helper without sending an encoding.
	if raw == nil {
		return nil, nil
	}
	if _, writeErr := p.writer.Write(append(raw, '\n')); writeErr != nil {
		return nil, errTokenizer
	}
	if p.writer.Flush() != nil {
		return nil, errTokenizer
	}
	// Bounded response, including offsets for the maximum source size.
	var rawResponse []byte
	for {
		piece, more, readErr := p.reader.ReadLine()
		if readErr != nil || len(rawResponse)+len(piece) > 16<<20 {
			return nil, errTokenizer
		}
		rawResponse = append(rawResponse, piece...)
		if !more {
			break
		}
	}
	if json.Unmarshal(rawResponse, &out) != nil || len(out) != count {
		return nil, errTokenizer
	}
	return out, nil
}

func (t *localTokenizer) Encode(ctx context.Context, inputs []tokenInput) ([]tokenEncoding, error) {
	// Keep the caller's cancellation channel at admission. Waiting calls hold no
	// serialized copy or helper goroutine; the hard deadline bounds the lease.
	deadline := time.Now().Add(10 * time.Second)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	if ctx.Err() != nil || len(inputs) > 512 {
		return nil, errTokenizer
	}
	var p *tokenizerProcess
	select {
	case <-ctx.Done():
		return nil, errTokenizer
	case <-timer.C:
		return nil, errTokenizer
	case <-t.closed:
		return nil, errTokenizer
	case p = <-t.slots:
	}
	release := true
	defer func() {
		if release {
			t.slots <- p
		}
	}()
	select {
	case <-t.closed:
		return nil, errTokenizer
	default:
	}
	if ctx.Err() != nil {
		return nil, errTokenizer
	}
	raw, err := json.Marshal(inputs)
	if err != nil || len(raw) > 4<<20 || ctx.Err() != nil {
		return nil, errTokenizer
	}
	// The lease outlives a cancelled caller until its response is drained.
	// Buffer the result so abandoned calls never retain a helper goroutine.
	type result struct {
		out []tokenEncoding
		err error
	}
	done := make(chan result, 1)
	release = false
	go func() {
		hardCtx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		out, err := p.exchange(hardCtx, raw, len(inputs))
		t.slots <- p
		done <- result{out, err}
	}()
	select {
	case <-ctx.Done():
		return nil, errTokenizer
	case <-t.closed:
		return nil, errTokenizer
	case r := <-done:
		if ctx.Err() != nil {
			return nil, errTokenizer
		}
		return r.out, r.err
	}
}

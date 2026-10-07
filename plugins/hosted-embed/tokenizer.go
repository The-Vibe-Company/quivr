package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"
)

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

// A single checksum-pinned local tokenizer stays loaded across documents.
// Source strings travel on pipes only; errors never expose them.
type localTokenizer struct {
	config tokenizerConfiguration
	mu     sync.Mutex
	cmd    *exec.Cmd
	writer *bufio.Writer
	reader *bufio.Reader
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

func (t *localTokenizer) stop() {
	if t.cmd != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
		t.cmd = nil
	}
}
func (t *localTokenizer) exchange(inputs []tokenInput) ([]tokenEncoding, error) {
	if t.cmd != nil {
		cmd := t.cmd
		timer := time.AfterFunc(10*time.Second, func() { _ = cmd.Process.Kill() })
		defer timer.Stop()
	}
	if t.cmd == nil {
		cmd := exec.Command(t.config.Python, "-u", "-c", tokenizerSource, t.config.Model, t.config.SHA256)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, errTokenizer
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, errTokenizer
		}
		if cmd.Start() != nil {
			return nil, errTokenizer
		}
		timer := time.AfterFunc(10*time.Second, func() { _ = cmd.Process.Kill() })
		defer timer.Stop()
		t.cmd = cmd
		t.writer = bufio.NewWriter(stdin)
		t.reader = bufio.NewReader(stdout)
		ready, err := t.reader.ReadString('\n')
		if err != nil || ready != "ready\n" {
			t.stop()
			return nil, errTokenizer
		}
	}
	raw, err := json.Marshal(inputs)
	if err != nil || len(raw) > 4<<20 {
		return nil, errTokenizer
	}
	if _, err = t.writer.Write(append(raw, '\n')); err != nil {
		t.stop()
		return nil, errTokenizer
	}
	if t.writer.Flush() != nil {
		t.stop()
		return nil, errTokenizer
	}
	// Bounded response, including offsets for the maximum source size.
	var rawResponse []byte
	for {
		piece, more, err := t.reader.ReadLine()
		if err != nil || len(rawResponse)+len(piece) > 16<<20 {
			t.stop()
			return nil, errTokenizer
		}
		rawResponse = append(rawResponse, piece...)
		if !more {
			break
		}
	}
	var out []tokenEncoding
	if json.Unmarshal(rawResponse, &out) != nil || len(out) != len(inputs) {
		t.stop()
		return nil, errTokenizer
	}
	return out, nil
}
func (t *localTokenizer) Encode(ctx context.Context, inputs []tokenInput) ([]tokenEncoding, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Each exchange has a hard deadline that kills a stuck helper. A cancelled
	// caller returns early; the bounded exchange completes before the next one.
	done := make(chan struct {
		out []tokenEncoding
		err error
	}, 1)
	go func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if ctx.Err() != nil {
			done <- struct {
				out []tokenEncoding
				err error
			}{nil, errTokenizer}
			return
		}
		out, err := t.exchange(inputs)
		done <- struct {
			out []tokenEncoding
			err error
		}{out, err}
	}()
	select {
	case result := <-done:
		return result.out, result.err
	case <-ctx.Done():
	}
	// exchange owns the process; its hard timer unblocks it without racing on
	// process state. No source/error payload escapes on cancellation.
	return nil, errTokenizer
}

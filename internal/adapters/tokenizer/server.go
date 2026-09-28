package tokenizer

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
)

// serverSource encodes exactly like scripts/token_offsets.py but loads the pinned
// tokenizer once. It is pinned by the build; parity tests hold it to the reference.
//
//go:embed server.py
var serverSource string

var errUnavailable = errors.New("tokenizer unavailable")

const (
	defaultTimeout     = 10 * time.Second
	defaultMaxResponse = 16 << 20
)

// Server keeps one pinned tokenizer process alive and serializes requests to it
// (THE-675): loading the 17 MB tokenizer dominated every search when a process
// was started per call. A failed or timed-out process is replaced on the next call.
// Use a pointer; the zero value with a Config is ready to use.
type Server struct {
	Config Config

	// Overridable by tests.
	source      string
	timeout     time.Duration
	maxResponse int

	once   sync.Once
	slot   chan struct{} // holds the right to use proc
	proc   *serverProcess
	closed bool
	spawns int
}

type serverProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

type exchangeResult struct {
	encodings []processing.Encoding
	err       error
}

func (s *Server) init() {
	s.once.Do(func() {
		s.slot = make(chan struct{}, 1)
		if s.source == "" {
			s.source = serverSource
		}
		if s.timeout == 0 {
			s.timeout = defaultTimeout
		}
		if s.maxResponse == 0 {
			s.maxResponse = defaultMaxResponse
		}
	})
}

func (s *Server) Encode(ctx context.Context, input []processing.TokenInput) ([]processing.Encoding, error) {
	b, err := request(input)
	if err != nil {
		return nil, err
	}
	// Keep failing closed when the configuration does not name the pinned reference.
	if _, err = pinnedScript(s.Config); err != nil {
		return nil, err
	}
	s.init()
	select {
	case s.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, errUnavailable
	}
	done := make(chan exchangeResult, 1)
	go func() {
		defer func() { <-s.slot }()
		encodings, err := s.exchange(b, len(input))
		done <- exchangeResult{encodings, err}
	}()
	// A cancelled caller returns at once; the exchange completes (or hits the hard
	// timeout) in the background so the warm process survives client disconnects.
	select {
	case r := <-done:
		return r.encodings, r.err
	case <-ctx.Done():
		return nil, errUnavailable
	}
}

// exchange runs while holding the slot.
func (s *Server) exchange(b []byte, items int) ([]processing.Encoding, error) {
	if s.closed {
		return nil, errUnavailable
	}
	if s.proc == nil {
		p, err := s.start()
		if err != nil {
			return nil, errUnavailable
		}
		s.proc = p
	}
	p := s.proc
	line, err := s.roundTrip(p, append(b, '\n'))
	// Never include subprocess output in errors: it may contain source content.
	if err != nil {
		s.stop()
		return nil, errUnavailable
	}
	if bytes.Equal(line, []byte("null")) {
		return nil, errUnavailable
	}
	var encodings []processing.Encoding
	if err = json.Unmarshal(line, &encodings); err != nil || len(encodings) != items {
		s.stop()
		return nil, errors.New("invalid tokenizer response")
	}
	return encodings, nil
}

func (s *Server) start() (*serverProcess, error) {
	s.spawns++
	cmd := exec.Command(s.Config.Python, "-c", s.source, s.Config.Model)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	p := &serverProcess{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 64<<10)}
	line, err := s.roundTrip(p, nil)
	if err != nil || string(line) != "ready" {
		kill(p)
		return nil, errUnavailable
	}
	return p, nil
}

// roundTrip writes an optional request and reads one response line under the hard timeout.
func (s *Server) roundTrip(p *serverProcess, request []byte) ([]byte, error) {
	timer := time.AfterFunc(s.timeout, func() { _ = p.cmd.Process.Kill() })
	var line []byte
	var err error
	if request != nil {
		_, err = p.stdin.Write(request)
	}
	if err == nil {
		line, err = readLine(p.stdout, s.maxResponse)
	}
	if !timer.Stop() {
		return nil, errUnavailable
	}
	return line, err
}

func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > limit+1 {
			return nil, errors.New("tokenizer response too large")
		}
		line = append(line, chunk...)
		if err == nil {
			return line[:len(line)-1], nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func (s *Server) stop() {
	if s.proc != nil {
		kill(s.proc)
		s.proc = nil
	}
}

func kill(p *serverProcess) {
	_ = p.stdin.Close()
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
}

// Close stops the tokenizer process after any in-flight request; later calls fail.
func (s *Server) Close() {
	s.init()
	s.slot <- struct{}{}
	defer func() { <-s.slot }()
	s.closed = true
	s.stop()
}

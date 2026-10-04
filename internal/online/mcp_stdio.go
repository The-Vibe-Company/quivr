package online

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// mcpDrainTimeout bounds how long `quivr mcp` waits, once its input is closed,
// for the answers still owed: as long as one API call may take.
const mcpDrainTimeout = requestTimeout

// mcpUntilClosed lists the requests the SDK answers only when the session ends,
// such as a stream of list-changed notifications. They are not owed answers:
// on end of input the SDK cancels them, as the client expects.
var mcpUntilClosed = map[string]bool{"subscriptions/listen": true}

// mcpStdio sits between stdin and stdout and the MCP SDK so that closing stdin
// does not drop answers. The SDK takes end of input as the client leaving: it
// cancels the requests still in flight and writes none of their answers, so a
// script that pipes requests and closes its input would get nothing back.
// mcpStdio passes every byte through unchanged, counts the requests read and
// the answers written, and holds end of input back from the SDK until each
// request has its answer, a write to stdout fails, ctx ends, or
// mcpDrainTimeout passes.
type mcpStdio struct {
	ctx context.Context
	in  io.Reader
	out io.Writer

	// copyIn feeds what the SDK reads to track, which counts the requests.
	copyIn  *io.PipeWriter
	tracked chan struct{}

	mu sync.Mutex
	// owed counts, per request ID, the requests read minus the answers written.
	// An answer can be written before track has counted its request.
	owed      map[jsonrpc.ID]int
	outBroken bool
	changed   chan struct{}
}

func newMCPStdio(ctx context.Context, in io.Reader, out io.Writer) *mcpStdio {
	r, w := io.Pipe()
	s := &mcpStdio{ctx: ctx, in: in, out: out, copyIn: w, tracked: make(chan struct{}), owed: map[jsonrpc.ID]int{}, changed: make(chan struct{}, 1)}
	go s.track(r)
	return s
}

// track counts the requests in the input stream. Input it cannot decode is
// the SDK's to report; track then stops counting and only drains the copy.
func (s *mcpStdio) track(r io.Reader) {
	defer close(s.tracked)
	dec := json.NewDecoder(r)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			_, _ = io.Copy(io.Discard, r)
			return
		}
		for _, msg := range jsonrpcMessages(raw) {
			if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() && !mcpUntilClosed[req.Method] {
				s.settle(req.ID, 1)
			}
		}
	}
}

func (s *mcpStdio) Read(p []byte) (int, error) {
	n, err := s.in.Read(p)
	if n > 0 {
		_, _ = s.copyIn.Write(p[:n])
		if err == io.EOF {
			// The SDK needs these last requests before any wait for their answers;
			// the next Read returns end of input again.
			return n, nil
		}
	}
	if err != nil {
		s.copyIn.Close()
	}
	if err == io.EOF {
		<-s.tracked
		if derr := s.drain(); derr != nil {
			return n, derr
		}
	}
	return n, err
}

// Write writes one message of the SDK, which writes each message, or batch of
// messages, in one call. Answers are settled before they are written, so a
// client reusing an ID once it has the answer is never mistaken for the first
// request: the SDK finishes a write it has started even after end of input.
func (s *mcpStdio) Write(p []byte) (int, error) {
	for _, msg := range jsonrpcMessages(p) {
		if resp, ok := msg.(*jsonrpc.Response); ok && resp.ID.IsValid() {
			s.settle(resp.ID, -1)
		}
	}
	n, err := s.out.Write(p)
	if err != nil {
		s.mu.Lock()
		s.outBroken = true
		s.mu.Unlock()
		s.signal()
	}
	return n, err
}

// settle counts a request read (+1) or an answer written (-1). An ID owes at
// most one answer: the SDK answers a request reusing the ID of one in flight
// with an error that carries no ID. An answer may come before track has
// counted its request, leaving the count at -1 until it does.
func (s *mcpStdio) settle(id jsonrpc.ID, delta int) {
	s.mu.Lock()
	if delta < 0 || s.owed[id] <= 0 {
		s.owed[id] += delta
	}
	if s.owed[id] == 0 {
		delete(s.owed, id)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *mcpStdio) signal() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// drain waits until no answer is owed. It gives up early when stdout is broken,
// since no answer can reach the client, or when ctx ends, which the SDK reports.
func (s *mcpStdio) drain() error {
	timeout := time.NewTimer(mcpDrainTimeout)
	defer timeout.Stop()
	for {
		s.mu.Lock()
		unanswered := 0
		for _, n := range s.owed {
			if n > 0 {
				unanswered++
			}
		}
		broken := s.outBroken
		s.mu.Unlock()
		if unanswered == 0 || broken {
			return nil
		}
		select {
		case <-s.changed:
		case <-s.ctx.Done():
			return nil
		case <-timeout.C:
			return fmt.Errorf("input closed with %d requests still unanswered after %s", unanswered, mcpDrainTimeout)
		}
	}
}

// jsonrpcMessages decodes one JSON-RPC message or batch, skipping what is not one.
func jsonrpcMessages(data []byte) []jsonrpc.Message {
	data = bytes.TrimSpace(data)
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		raws = []json.RawMessage{data}
	}
	var msgs []jsonrpc.Message
	for _, raw := range raws {
		if msg, err := jsonrpc.DecodeMessage(raw); err == nil {
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

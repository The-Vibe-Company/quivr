// Package embedding models the OpenAI embeddings and Cohere v2 embed wire
// contracts. It never forwards a request to another server.
package embedding

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

type Call struct {
	Auth       string   `json:"auth"`
	Format     string   `json:"format"`
	Model      string   `json:"model"`
	Texts      []string `json:"texts"`
	InputType  string   `json:"input_type,omitempty"`
	Dimensions int      `json:"dimensions"`
}

type Server struct {
	mu       sync.Mutex
	calls    []Call
	statuses []int
	// Fault deliberately violates a response contract for consumer tests.
	Fault string
}

func New() *Server { return &Server{} }
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}
func (s *Server) Enqueue(statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, statuses...)
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_fake/stats" {
		write(w, 200, s.Calls())
		return
	}
	if r.URL.Path == "/_control" && r.Method == "POST" {
		var c struct {
			Statuses []int `json:"statuses"`
		}
		if json.NewDecoder(r.Body).Decode(&c) != nil {
			write(w, 400, map[string]string{"error": "invalid control"})
			return
		}
		s.Enqueue(c.Statuses...)
		write(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != "POST" || (!strings.HasSuffix(r.URL.Path, "/embeddings") && !strings.HasSuffix(r.URL.Path, "/embed")) {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake-key" && r.Header.Get("api-key") != "fake-key" {
		write(w, 401, map[string]string{"error": "missing fake credential"})
		return
	}
	var req struct {
		Model           string   `json:"model"`
		Input           []string `json:"input"`
		Texts           []string `json:"texts"`
		Dimensions      int      `json:"dimensions"`
		OutputDimension int      `json:"output_dimension"`
		InputType       string   `json:"input_type"`
		EmbeddingTypes  []string `json:"embedding_types"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model == "" {
		write(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	auth := "bearer"
	if r.Header.Get("api-key") == "fake-key" {
		auth = "api-key"
	}
	call := Call{Auth: auth, Format: "openai", Model: req.Model, Texts: req.Input, Dimensions: req.Dimensions}
	if strings.HasSuffix(r.URL.Path, "/embed") {
		call.Format = "cohere"
		call.Texts = req.Texts
		call.Dimensions = req.OutputDimension
		call.InputType = req.InputType
		if (req.InputType != "search_query" && req.InputType != "search_document") || len(req.EmbeddingTypes) != 1 || req.EmbeddingTypes[0] != "float" || len(req.Input) > 0 {
			write(w, 400, map[string]string{"error": "invalid Cohere mode or representation"})
			return
		}
	}
	if len(call.Texts) == 0 || len(call.Texts) > 32 || call.Dimensions < 1 || call.Dimensions > 4096 {
		write(w, 400, map[string]string{"error": "invalid batch or dimensions"})
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	status := 200
	if len(s.statuses) > 0 {
		status = s.statuses[0]
		s.statuses = s.statuses[1:]
	}
	fault := s.Fault
	s.mu.Unlock()
	if status != 200 {
		w.Header().Set("Retry-After", "0")
		write(w, status, map[string]string{"error": "scripted provider error"})
		return
	}
	vectors := make([][]float64, len(call.Texts))
	tokens := 0
	for k, text := range call.Texts {
		// Word hashing is only for the fake stack's semantic search, never a
		// claim about a real model's quality. Remove the documented E5 templates.
		text = strings.TrimPrefix(strings.TrimPrefix(text, "query: "), "passage: ")
		vectors[k] = make([]float64, call.Dimensions)
		for _, word := range strings.Fields(strings.ToLower(text)) {
			sum := sha256.Sum256([]byte(word))
			vectors[k][int(sum[0])%len(vectors[k])]++
		}
		if len(strings.Fields(text)) == 0 {
			vectors[k][0] = 1
		}
		tokens += len(call.Texts[k])
	}
	switch fault {
	case "count":
		vectors = nil
	case "dimensions":
		vectors[0] = append(vectors[0], 1)
	case "zero":
		clear(vectors[0])
	case "overflow":
		vectors[0][0] = 1e100
	}
	if call.Format == "cohere" {
		write(w, 200, map[string]any{"embeddings": map[string]any{"float": vectors}, "meta": map[string]any{"billed_units": map[string]int{"input_tokens": tokens}}})
		return
	}
	data := make([]map[string]any, len(vectors))
	for k, v := range vectors {
		index := k
		if fault == "duplicate_index" {
			index = -1
		}
		data[k] = map[string]any{"index": index, "embedding": v}
	}
	// Reverse data to ensure clients use index, rather than array position.
	for left, right := 0, len(data)-1; left < right; left, right = left+1, right-1 {
		data[left], data[right] = data[right], data[left]
	}
	write(w, 200, map[string]any{"data": data, "usage": map[string]int{"prompt_tokens": tokens, "total_tokens": tokens}})
}

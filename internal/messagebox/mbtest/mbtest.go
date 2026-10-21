// Package mbtest is a stand-in BRC-33 message box server for tests: the
// three calls in the shapes of the HTTP conformance vectors, with the
// caller's identity taken from the header the vectors show instead of from
// a mutual authentication. Nothing here is for real use.
package mbtest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"

	authhttp "github.com/bsv-blockchain/go-sdk/auth/clients/authhttp"

	"github.com/lightwebinc/bfinger/internal/messagebox"
)

// Server is a message box server reduced to the three calls, in the shapes
// of the BRC-33 HTTP conformance vectors: the caller's identity is what
// mutual authentication established, carried here in the header the vectors
// show, x-bsv-auth-identity-key.
type Server struct {
	mu    sync.Mutex
	boxes map[string]map[string][]messagebox.Message // recipient, box
	Seen  []map[string]any
	Price bool
}

func NewServer() *Server { return &Server{boxes: map[string]map[string][]messagebox.Message{}} }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	caller := r.Header.Get("x-bsv-auth-identity-key")
	if caller == "" {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"status":"error","code":"ERR_AUTH_REQUIRED"}`))
		return
	}
	var in map[string]any
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Seen = append(s.Seen, in)
	ok := func(extra map[string]any) {
		out := map[string]any{"status": "success"}
		for k, v := range extra {
			out[k] = v
		}
		_ = json.NewEncoder(w).Encode(out)
	}
	switch r.URL.Path {
	case "/sendMessage":
		if s.Price {
			w.WriteHeader(402)
			return
		}
		m, _ := in["message"].(map[string]any)
		rcpt, _ := m["recipient"].(string)
		box, _ := m["messageBox"].(string)
		id, _ := m["messageId"].(string)
		body, _ := m["body"].(string)
		if rcpt == "" || box == "" || id == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"status":"error"}`))
			return
		}
		if s.boxes[rcpt] == nil {
			s.boxes[rcpt] = map[string][]messagebox.Message{}
		}
		for _, have := range s.boxes[rcpt][box] {
			if have.MessageID == id {
				ok(nil)
				return
			}
		}
		s.boxes[rcpt][box] = append(s.boxes[rcpt][box], messagebox.Message{MessageID: id, Body: body, Sender: caller})
		ok(map[string]any{"results": []map[string]any{{"recipient": rcpt, "messageId": id}}})
	case "/listMessages":
		box, _ := in["messageBox"].(string)
		msgs := s.boxes[caller][box]
		if msgs == nil {
			msgs = []messagebox.Message{}
		}
		ok(map[string]any{"messages": msgs})
	case "/acknowledgeMessage":
		box, _ := in["messageBox"].(string)
		ids, _ := in["messageIds"].([]any)
		drop := map[string]bool{}
		for _, id := range ids {
			if s, ok := id.(string); ok {
				drop[s] = true
			}
		}
		var keep []messagebox.Message
		for _, m := range s.boxes[caller][box] {
			if !drop[m.MessageID] {
				keep = append(keep, m)
			}
		}
		if s.boxes[caller] != nil {
			s.boxes[caller][box] = keep
		}
		ok(nil)
	default:
		http.NotFound(w, r)
	}
}

// As is a Fetcher that reaches srv as identity, standing in for AuthFetch.
func As(base, identity string) messagebox.Fetcher { return plain{base, identity} }

type plain struct{ base, id string }

func (p plain) Fetch(ctx context.Context, url string, cfg *authhttp.SimplifiedFetchRequestOptions) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, cfg.Method, url, bytes.NewReader(cfg.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("x-bsv-auth-identity-key", p.id)
	return http.DefaultClient.Do(req)
}

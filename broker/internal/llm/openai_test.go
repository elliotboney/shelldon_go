package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestComplete_ClassifiesTransientErrors proves a 5xx response is marked
// ErrTransient (retryable) while a 4xx is not — so the broker retries only
// transient failures.
func TestComplete_ClassifiesTransientErrors(t *testing.T) {
	cases := []struct {
		status      int
		wantTransient bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusUnauthorized, false},
		{http.StatusBadRequest, false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"x"}}`))
		}))
		p := NewOpenAI("glm", srv.URL, "glm-test", srv.Client())
		_, err := p.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, "")
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected an error", c.status)
		}
		if got := errors.Is(err, ErrTransient); got != c.wantTransient {
			t.Errorf("status %d: ErrTransient=%v, want %v (err=%v)", c.status, got, c.wantTransient, err)
		}
	}
}

// TestOpenAIProvider_BaseURLSwap is AC3: the go-openai client is pointed at the
// configured base URL (the GLM base-URL swap) — the request reaches that server's
// chat-completions endpoint and the reply is parsed back. Uses httptest, so no
// real GLM endpoint and no credits.
func TestOpenAIProvider_BaseURLSwap(t *testing.T) {
	var hitPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "cmpl-test",
			"object":  "chat.completion",
			"model":   "glm-test",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "swapped-ok"}}},
		})
	}))
	defer srv.Close()

	p := NewOpenAI("glm", srv.URL, "glm-test", srv.Client())
	text, err := p.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, "")
	if err != nil {
		t.Fatalf("Complete via swapped base URL: %v", err)
	}
	if text != "swapped-ok" {
		t.Fatalf("reply = %q, want %q", text, "swapped-ok")
	}
	if got, _ := hitPath.Load().(string); got != "/chat/completions" {
		t.Fatalf("request hit %q, want the chat-completions endpoint on the swapped base URL", got)
	}
}

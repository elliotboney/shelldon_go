package broker

import (
	"context"
	"errors"
)

// Message is one chat turn in a completion request. Role is "system"/"user"/
// "assistant"; Content is the text. Broker-local (SDK-free) so go-openai types
// never leak out of broker/internal/ (AD-9).
type Message struct {
	Role    string
	Content string
}

// Request is a model-egress completion request the worker (Story 3.3) sends to
// the broker. Model is optional; empty means the provider's configured default.
type Request struct {
	Model    string
	Messages []Message
}

// Response is the model's reply text. Memory-ops and streaming are later stories.
type Response struct {
	Text string
}

// Provider is one LLM backend in the broker's ordered chain. Implementations live
// in broker/internal/llm (go-openai); tests inject fakes. The interface uses only
// broker-local types, so broker/ composes the chain without importing any SDK.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
}

// ErrAllProvidersFailed is returned by Broker.Complete when every provider in the
// chain is exhausted. The arbiter degrades this to a reflex behavior (Story 2.6),
// so the pet never freezes (AD-8).
var ErrAllProvidersFailed = errors.New("broker: all providers failed")

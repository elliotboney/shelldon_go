// Package llm holds the provider SDK implementations behind the broker's trust
// boundary (AD-9). It is the only place github.com/sashabaranov/go-openai may be
// imported — Go's internal/ rule plus the depguard fence keep the SDK out of the
// rest of the tree. The package is a leaf: it imports no broker types, so the
// broker adapts its small primitive surface into the broker.Provider interface.
package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	openai "github.com/sashabaranov/go-openai"
)

// ErrTransient marks an error worth retrying: a transport-level failure (network,
// timeout) or a 5xx response. The broker's retry policy retries only errors that
// wrap ErrTransient, so a 4xx (auth/quota/bad-request) falls through to the next
// provider immediately instead of burning retries. Classification lives here
// because only this package may inspect go-openai's error types (the fence).
var ErrTransient = errors.New("llm: transient error")

// classify wraps err in ErrTransient when it is a 5xx APIError or a transport
// RequestError; otherwise it returns err unchanged (4xx and local errors are not
// retried).
func classify(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode >= 500 {
		return fmt.Errorf("%w: %w", ErrTransient, err)
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return fmt.Errorf("%w: %w", ErrTransient, err)
	}
	return err
}

// ChatMessage is the llm package's own SDK-free message shape. The broker
// translates its own Message into this at the adapter boundary.
type ChatMessage struct {
	Role    string
	Content string
}

// OpenAIProvider is an OpenAI-compatible LLM provider. GLM, OpenAI, OpenRouter,
// and Ollama-LAN are all reached through the same client by swapping the base URL
// (AD-9). Auth is injected by the http.Client's transport (the broker's
// authtransport), so the go-openai config Token stays empty — the key never
// enters the SDK.
type OpenAIProvider struct {
	name   string
	model  string
	client *openai.Client
}

// NewOpenAI builds an OpenAI-compatible provider pointed at baseURL using
// httpClient (the broker's pre-authorized client). model is the default model id.
func NewOpenAI(name, baseURL, model string, httpClient *http.Client) *OpenAIProvider {
	cfg := openai.DefaultConfig("") // empty token: auth rides httpClient's transport (AD-9)
	cfg.BaseURL = baseURL
	cfg.HTTPClient = httpClient
	return &OpenAIProvider{name: name, model: model, client: openai.NewClientWithConfig(cfg)}
}

// Name identifies the provider in logs and the chain.
func (p *OpenAIProvider) Name() string { return p.name }

// Complete runs one non-streaming chat completion. model overrides the provider
// default when non-empty. It returns the first choice's content, or the SDK error
// unchanged so the broker chain decides fallback.
func (p *OpenAIProvider) Complete(ctx context.Context, msgs []ChatMessage, model string) (string, error) {
	if model == "" {
		model = p.model
	}
	oaMsgs := make([]openai.ChatCompletionMessage, len(msgs))
	for i, m := range msgs {
		oaMsgs[i] = openai.ChatCompletionMessage{Role: m.Role, Content: m.Content}
	}
	resp, err := p.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{Model: model, Messages: oaMsgs})
	if err != nil {
		return "", classify(err) // mark 5xx/transport errors retryable (ErrTransient)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("llm: completion returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

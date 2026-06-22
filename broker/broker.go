// Package broker is the sole trust boundary (AD-9): the only holder of model/tool
// credentials and the only egress to models. Credentials resolve only here, from
// the environment — never from the bus, never in source. The broker exposes only
// a pre-authorized *http.Client (auth injected by broker/internal/authtransport)
// and a Complete method that runs an ordered provider chain with retry/fallback
// (failsafe-go); downstream callers (the worker, Story 3.3) never see the raw key.
// No credential ever rides the bus (NFR8): Job/Result carry none, the broker
// injects them internally at egress.
//
// Provider SDKs live only under broker/internal/llm, enforced by depguard
// (.golangci.yml) and Go's internal/ rule; the broker composes the chain through
// the SDK-free Provider interface (provider.go).
package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/elliotboney/shelldon_go/broker/internal/authtransport"
	"github.com/elliotboney/shelldon_go/broker/internal/llm"
	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"github.com/failsafe-go/failsafe-go/timeout"
)

// Credential + provider config (tunable story-time config, not spine invariants).
// The default provider is GLM via an OpenAI-compatible base-URL swap (AD-9); the
// base URL, model, and key all resolve from the environment.
const (
	apiKeyEnv  = "SHELLDON_LLM_API_KEY"
	baseURLEnv = "SHELLDON_LLM_BASE_URL"
	modelEnv   = "SHELLDON_LLM_MODEL"

	defaultProviderName = "glm"
	defaultBaseURL      = "https://open.bigmodel.cn/api/paas/v4" // GLM OpenAI-compatible endpoint
	defaultModel        = "glm-4-flash"
)

// Resilience policy config for each provider attempt (tunable).
const (
	maxRetries         = 2
	retryDelay         = 100 * time.Millisecond
	perProviderTimeout = 30 * time.Second
)

// Broker holds the pre-authorized client (the key lives in its unexported
// transport) and the ordered provider chain.
type Broker struct {
	client *http.Client
	chain  []Provider
}

// New resolves the model credential from the environment, builds the
// pre-authorized client, and wires the default GLM provider as the chain head. A
// missing credential is not fatal: the client carries an empty bearer and the
// absence is logged (AD-17) so the chain fails at request time and degrades to
// reflex rather than crashing (AD-8). The key value is never logged.
func New() *Broker {
	key := os.Getenv(apiKeyEnv)
	if key == "" {
		slog.Warn("broker: no model credential resolved; LLM egress unavailable", "env", apiKeyEnv)
	} else {
		slog.Info("broker: model credential resolved", "env", apiKeyEnv)
	}
	client := &http.Client{Transport: authtransport.New(key, nil)}

	baseURL := os.Getenv(baseURLEnv)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	model := os.Getenv(modelEnv)
	if model == "" {
		model = defaultModel
	}
	defaultProvider := openaiAdapter{p: llm.NewOpenAI(defaultProviderName, baseURL, model, client)}

	return &Broker{client: client, chain: []Provider{defaultProvider}}
}

// Client returns the pre-authorized HTTP client — the AD-9 idiom. The client
// injects auth on every request; there is no exported path to the raw key
// (NFR8). The providers are built from it; tool egress will use it later.
func (b *Broker) Client() *http.Client { return b.client }

// Complete runs req through the ordered provider chain. Each provider gets a
// bounded retry + timeout (failsafe-go); on exhaustion the chain advances to the
// next provider. The first success returns; if every provider fails, it returns
// ErrAllProvidersFailed wrapping the last error — the arbiter degrades that to a
// reflex behavior (Story 2.6), so the pet never freezes (AD-8/AD-9).
func (b *Broker) Complete(ctx context.Context, req Request) (Response, error) {
	var lastErr error
	for i, p := range b.chain {
		// Retry only transient errors (5xx / transport); a 4xx falls through at
		// once instead of burning retries. The Timeout policy cancels the
		// execution context, and GetWithExecution threads that context into the
		// provider call so a timeout aborts the in-flight HTTP request (AD-8),
		// not merely the next retry.
		retry := retrypolicy.NewBuilder[Response]().
			WithMaxRetries(maxRetries).
			WithDelay(retryDelay).
			HandleIf(func(_ Response, err error) bool { return errors.Is(err, llm.ErrTransient) }).
			Build()
		executor := failsafe.With[Response](retry, timeout.New[Response](perProviderTimeout)).WithContext(ctx)

		resp, err := executor.GetWithExecution(func(exec failsafe.Execution[Response]) (Response, error) {
			return p.Complete(exec.Context(), req)
		})
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if i < len(b.chain)-1 {
			slog.Warn("broker: provider failed, advancing chain", "provider", p.Name(), "err", err)
		}
	}
	if lastErr == nil {
		return Response{}, ErrAllProvidersFailed // no providers configured
	}
	slog.Error("broker: all providers exhausted", "providers", len(b.chain), "last_err", lastErr)
	return Response{}, fmt.Errorf("%w: %w", ErrAllProvidersFailed, lastErr)
}

// openaiAdapter adapts a broker/internal/llm OpenAI provider to the broker-local
// Provider interface, translating between broker and llm message types. This is
// the anti-corruption boundary that keeps llm a pure go-openai leaf (no broker
// import, no import cycle).
type openaiAdapter struct {
	p *llm.OpenAIProvider
}

func (a openaiAdapter) Name() string { return a.p.Name() }

func (a openaiAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	msgs := make([]llm.ChatMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = llm.ChatMessage{Role: m.Role, Content: m.Content}
	}
	text, err := a.p.Complete(ctx, msgs, req.Model)
	if err != nil {
		return Response{}, err
	}
	return Response{Text: text}, nil
}

package broker_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/elliotboney/shelldon_go/broker"
)

// fakeProvider is a fault-injecting broker.Provider test double: it records its
// call count and returns the configured response/error.
type fakeProvider struct {
	name  string
	resp  broker.Response
	err   error
	calls *atomic.Int32
}

func (f fakeProvider) Name() string { return f.name }

func (f fakeProvider) Complete(_ context.Context, _ broker.Request) (broker.Response, error) {
	f.calls.Add(1)
	return f.resp, f.err
}

func userReq() broker.Request {
	return broker.Request{Messages: []broker.Message{{Role: "user", Content: "hi"}}}
}

// TestComplete_FallsBackToNextProvider is AC1: when the first provider fails, the
// chain advances and the turn completes via the next provider.
func TestComplete_FallsBackToNextProvider(t *testing.T) {
	var c1, c2 atomic.Int32
	p1 := fakeProvider{name: "p1", err: errors.New("HTTP 500"), calls: &c1}
	p2 := fakeProvider{name: "p2", resp: broker.Response{Text: "reply from p2"}, calls: &c2}

	resp, err := broker.NewWithProviders(p1, p2).Complete(context.Background(), userReq())
	if err != nil {
		t.Fatalf("Complete errored despite a healthy fallback provider: %v", err)
	}
	if resp.Text != "reply from p2" {
		t.Fatalf("reply = %q, want the fallback provider's reply", resp.Text)
	}
	if c1.Load() == 0 {
		t.Error("first provider was never attempted")
	}
	if c2.Load() == 0 {
		t.Error("fallback provider was never reached")
	}
}

// TestComplete_AllProvidersFail is AC2: when every provider fails, Complete returns
// ErrAllProvidersFailed (no panic, no hang) — the error the arbiter degrades to a
// reflex behavior on (Story 2.6).
func TestComplete_AllProvidersFail(t *testing.T) {
	var c1, c2 atomic.Int32
	p1 := fakeProvider{name: "p1", err: errors.New("HTTP 500"), calls: &c1}
	p2 := fakeProvider{name: "p2", err: errors.New("timeout"), calls: &c2}

	_, err := broker.NewWithProviders(p1, p2).Complete(context.Background(), userReq())
	if !errors.Is(err, broker.ErrAllProvidersFailed) {
		t.Fatalf("Complete returned %v, want ErrAllProvidersFailed", err)
	}
	if c1.Load() == 0 || c2.Load() == 0 {
		t.Error("not every provider in the chain was attempted before giving up")
	}
}

// TestClient_InjectsResolvedCredential is AC1: New() resolves the credential from
// the environment and Client() returns a pre-authorized client that injects it —
// callers send a plain request and the broker's transport adds the auth.
func TestClient_InjectsResolvedCredential(t *testing.T) {
	t.Setenv("SHELLDON_LLM_API_KEY", "sk-from-env")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	resp, err := broker.New().Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("request through broker client: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if want := "Bearer sk-from-env"; gotAuth != want {
		t.Fatalf("broker client injected %q, want %q", gotAuth, want)
	}
}

// TestNew_MissingCredentialDoesNotPanic is AC1's degraded path: with no env
// credential the broker still constructs (logs the absence; the worker/provider
// chain decides fallback) rather than panicking.
func TestNew_MissingCredentialDoesNotPanic(t *testing.T) {
	t.Setenv("SHELLDON_LLM_API_KEY", "")

	b := broker.New()
	if b.Client() == nil {
		t.Fatal("broker with no credential returned a nil client")
	}
}

// TestBroker_ExposesNoRawKeyAccessor is AC1 (NFR8): the broker's exported API must
// be Client() only — no exported field or method may surface the raw key. This
// fails if a future change adds a credential-shaped exported member.
func TestBroker_ExposesNoRawKeyAccessor(t *testing.T) {
	// The public surface is asserted by reflection over exported methods and the
	// (zero) exported fields of *Broker.
	bt := reflect.TypeOf(broker.New())

	for i := 0; i < bt.NumMethod(); i++ {
		name := strings.ToLower(bt.Method(i).Name)
		for _, bad := range []string{"key", "secret", "token", "credential", "auth"} {
			if strings.Contains(name, bad) {
				t.Errorf("Broker exposes method %q — must not surface the raw credential (NFR8)", bt.Method(i).Name)
			}
		}
	}

	// Broker holds only unexported fields; any exported field is a leak risk.
	elem := bt.Elem()
	for i := 0; i < elem.NumField(); i++ {
		if f := elem.Field(i); f.IsExported() {
			t.Errorf("Broker has exported field %q — credential machinery must stay unexported (NFR8)", f.Name)
		}
	}
}

package privsep

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
)

// blockSentinel makes the stub inner worker hang, so the parent's
// context-cancel teardown path can be exercised against a real subprocess.
const blockSentinel = "__block__"

// stubInner is the worker the child hosts under test. It is deterministic on the
// turn so the goroutine path and the privsep (subprocess) path produce identical
// Results — that equality IS the "transport swap reshapes no caller" proof (AC1).
type stubInner struct{}

func (stubInner) AssembleAndPropose(_ context.Context, turn contracts.Job) (contracts.Result, error) {
	switch {
	case turn.Input == blockSentinel:
		// Hang until the parent cancels and kills this process. A timed sleep (not
		// select{}) keeps a pending timer so the child runtime's deadlock detector
		// does not fire before the parent's teardown kill lands.
		time.Sleep(10 * time.Second)
		return contracts.Result{}, nil
	case turn.Kind == contracts.JobDream:
		return contracts.Result{MemoryOps: []contracts.MemoryOp{
			{Kind: contracts.MemoryOpPromoteLearning, PatternKey: turn.Input, Observation: "obs:" + turn.Input},
		}}, nil
	default:
		return contracts.Result{Reply: "reply:" + turn.Input}, nil
	}
}

// TestMain doubles as the child entry: when this test binary is re-exec'd with
// the sentinel env (by privsep.New's default command), it runs the worker loop
// over the inherited socket with stubInner and exits — so the parent Worker under
// test talks to a real subprocess over the actual socketpair + fd-inheritance +
// gob wire, not an in-process fake.
func TestMain(m *testing.M) {
	if IsChild() {
		if err := ChildMain(context.Background(), stubInner{}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestPrivsep_DualTransportMatchesGoroutine is the AC1 proof: for every turn
// shape that crosses the wire, the privsep subprocess path returns a Result equal
// to the in-process goroutine path — the swap reshapes no caller (AD-2/AD-4).
func TestPrivsep_DualTransportMatchesGoroutine(t *testing.T) {
	cases := []contracts.Job{
		{Input: "hello", ConvoID: "c1", Kind: contracts.JobReply},
		{Input: "patternX", ConvoID: "c2", Kind: contracts.JobDream},
	}

	pw := New()
	t.Cleanup(func() { _ = pw.Close() })

	for _, job := range cases {
		want, wantErr := stubInner{}.AssembleAndPropose(context.Background(), job) // goroutine transport
		got, gotErr := pw.AssembleAndPropose(context.Background(), job)            // gob/UDS transport

		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("job %+v: err mismatch: goroutine=%v privsep=%v", job, wantErr, gotErr)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("job %+v: result mismatch\n goroutine: %#v\n   privsep: %#v", job, want, got)
		}
	}
}

// TestPrivsep_RecyclesAcrossTurns proves the subprocess is recycled, not respawned
// per turn (AC2): two sequential turns succeed on one Worker without an error
// teardown between them.
func TestPrivsep_RecyclesAcrossTurns(t *testing.T) {
	pw := New()
	t.Cleanup(func() { _ = pw.Close() })

	for i, in := range []string{"first", "second"} {
		got, err := pw.AssembleAndPropose(context.Background(), contracts.Job{Input: in})
		if err != nil {
			t.Fatalf("turn %d (%q): %v", i, in, err)
		}
		if got.Reply != "reply:"+in {
			t.Fatalf("turn %d: reply = %q, want %q", i, got.Reply, "reply:"+in)
		}
	}
}

// TestPrivsep_ContextCancelTearsDownAndRecovers proves a cancelled mid-turn
// context returns promptly with the ctx error (AD-8) and tears the child down,
// and that the next turn lazily respawns a clean child (AC2).
func TestPrivsep_ContextCancelTearsDownAndRecovers(t *testing.T) {
	pw := New()
	t.Cleanup(func() { _ = pw.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := pw.AssembleAndPropose(ctx, contracts.Job{Input: blockSentinel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancel did not return promptly: took %v", elapsed)
	}

	// The next turn must respawn a fresh child and succeed.
	got, err := pw.AssembleAndPropose(context.Background(), contracts.Job{Input: "after"})
	if err != nil {
		t.Fatalf("post-cancel respawn turn failed: %v", err)
	}
	if got.Reply != "reply:after" {
		t.Fatalf("post-cancel reply = %q, want %q", got.Reply, "reply:after")
	}
}

// TestApplyCredential_GateDecision asserts the uid-drop gate decision (AC3)
// without requiring root: an unconfigured uid never sets a credential, and a
// configured uid on a non-root/non-linux host falls back to same-uid (no
// credential set). The actual OS-enforced drop is proven on the Pi in Story 5.2.
func TestApplyCredential_GateDecision(t *testing.T) {
	unconfigured := exec.Command("true")
	applyCredential(unconfigured, 0, 0)
	if unconfigured.SysProcAttr != nil {
		t.Fatal("uid=0 (unconfigured) must not set a credential")
	}

	configured := exec.Command("true")
	applyCredential(configured, 1000, 1000)
	if os.Geteuid() != 0 && configured.SysProcAttr != nil {
		t.Fatal("non-root parent must fall back to same-uid (no credential set)")
	}
}

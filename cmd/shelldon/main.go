// Command shelldon is the single supervised process: it wires the bus, arbiter,
// real LLM-backed worker (the Monolith+ worker over the broker — a keyless broker
// degrades to a reflex acknowledgement via the Story 2.6 path, so the pet still
// runs offline), personality-state checkpoint, core dispatch loop, a selectable
// chat-transport adapter (CLI by default; Telegram via SHELLDON_TRANSPORT=telegram
// — AD-12 pluggable transport, the adapter holds its own bot-token credential),
// terminal face renderer, and the tier-shaped scheduler (running the blink +
// mood-drift reflex jobs and the LLM-driven proactive-ping + dream-cycle turn jobs), then runs
// them as supervised edges under the core suture root until a shutdown signal
// arrives, draining edges in reverse start order (AD-5).
package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/elliotboney/shelldon_go/broker"
	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/arbiter"
	"github.com/elliotboney/shelldon_go/core/bus"
	"github.com/elliotboney/shelldon_go/core/compositor"
	"github.com/elliotboney/shelldon_go/core/dispatch"
	"github.com/elliotboney/shelldon_go/core/dream"
	"github.com/elliotboney/shelldon_go/core/memory"
	"github.com/elliotboney/shelldon_go/core/proactive"
	"github.com/elliotboney/shelldon_go/core/reflexes"
	"github.com/elliotboney/shelldon_go/core/scheduler"
	"github.com/elliotboney/shelldon_go/core/state"
	"github.com/elliotboney/shelldon_go/core/supervisor"
	"github.com/elliotboney/shelldon_go/core/turntier"
	"github.com/elliotboney/shelldon_go/display/terminal"
	"github.com/elliotboney/shelldon_go/transport/cli"
	"github.com/elliotboney/shelldon_go/transport/telegram"
	"github.com/elliotboney/shelldon_go/worker/monolith"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := bus.New()
	// turnTimeout bounds a worker turn so an absent/slow brain degrades to a reflex
	// acknowledgement instead of freezing the pet (AD-8/NFR13). It now bounds the
	// real LLM call: a timeout cancels the in-flight broker request (Story 3.2/3.3).
	const turnTimeout = 30 * time.Second
	// The broker is the sole credential holder + model egress (AD-9); construct it
	// once and back the real worker with it. New() logs credential presence/absence
	// (AD-17); a keyless broker returns ErrAllProvidersFailed at call time, which
	// the arbiter degrades to a reflex ack (AD-8) — the pet runs offline. The worker
	// is constructed below, once the memory layers it reads from exist.
	b := broker.New()

	// Personality-state: restore from the RAM checkpoint, or defaults on first
	// boot (AD-16). The checkpoint lives beside, not inside, the Epic 4 durable
	// memory layers (~/.shelldon/memory, ~/.shelldon/history.db).
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("resolve home dir", "err", err)
		os.Exit(1)
	}
	shelldonDir := filepath.Join(home, ".shelldon")
	if err := os.MkdirAll(shelldonDir, 0o755); err != nil {
		slog.Error("create ~/.shelldon", "err", err)
		os.Exit(1)
	}
	statePath := filepath.Join(shelldonDir, "state.json")
	store := state.New(state.Load(statePath), statePath)

	// Durable memory (AD-7): the sqlite conversation store + the curated markdown
	// tree, both under ~/.shelldon. The worker reads them read-only to ground its
	// prompts (AD-6/Story 4.4); core (dispatch) records each turn into the store.
	mem, err := memory.Open(filepath.Join(shelldonDir, "history.db"))
	if err != nil {
		slog.Error("open memory store", "err", err)
		os.Exit(1)
	}
	defer func() { _ = mem.Close() }()
	curated, err := memory.OpenCurated(filepath.Join(shelldonDir, "memory"))
	if err != nil {
		slog.Error("open curated memory", "err", err)
		os.Exit(1)
	}
	const recentWindowN = 10 // recent-conversation window injected into each prompt (tunable)
	memCtx := memory.NewContext(mem, curated, recentWindowN)

	// The Monolith+ worker over the broker, memory-augmented (Story 4.4): it
	// assembles DIRECTIVE + about + recent window into each prompt (AD-7) through the
	// read-only context seam, then proposes a reply. The arbiter bounds the turn.
	w := monolith.New(b, monolith.WithContextSource(memCtx))
	arb := arbiter.New(w, turnTimeout)

	inbound := make(chan contracts.Envelope, 16)
	outbound := make(chan contracts.Envelope, 16)
	if err := hub.Register(contracts.KindInboundMessage, inbound); err != nil {
		slog.Error("register inbound route", "err", err)
		os.Exit(1)
	}
	if err := hub.Register(contracts.KindOutboundMessage, outbound); err != nil {
		slog.Error("register outbound route", "err", err)
		os.Exit(1)
	}
	display := make(chan contracts.Envelope, 16)
	if err := hub.Register(contracts.KindFaceSnapshot, display); err != nil {
		slog.Error("register face-snapshot route", "err", err)
		os.Exit(1)
	}

	disp := dispatch.New(hub, arb, inbound, store, dispatch.WithRecorder(mem))

	// Transport selection (AD-12): exactly one chat adapter is wired. The bus is
	// point-to-point, so only the selected adapter registers on the outbound route.
	// CLI is the default; Telegram is opted in via SHELLDON_TRANSPORT=telegram and
	// holds its own bot-token credential (never a broker model/tool cred, AD-9). A
	// Telegram misconfiguration (e.g. no token) degrades to reflex-only under
	// supervision rather than crashing core (AD-5/AD-12) — the rest of the pet runs.
	transportName := "cli-transport"
	// ownerConvoID is the conversation a proactive ping targets. CLI renders any
	// outbound regardless of ConvoID, so "cli" works; Telegram maps ConvoID → chat
	// id, so use the configured owner chat.
	ownerConvoID := "cli"
	var transportServe func(ctx context.Context) error
	switch os.Getenv("SHELLDON_TRANSPORT") {
	case "telegram":
		transportName = "telegram-transport"
		ownerConvoID = os.Getenv("SHELLDON_TELEGRAM_OWNER_ID")
		tg, terr := telegram.NewFromEnv(hub, outbound)
		if terr != nil {
			slog.Error("telegram transport unavailable; degrading to reflex-only", "err", terr)
			transportServe = func(ctx context.Context) error {
				<-ctx.Done() // dormant until shutdown: a missing token won't fix itself, so don't crash-loop the supervisor (AD-5 degrade-to-reflex-only)
				return ctx.Err()
			}
		} else {
			transportServe = tg.Serve
		}
	default:
		cliAdapter := cli.New(hub, outbound, os.Stdin, os.Stdout, "cli")
		transportServe = cliAdapter.Serve
	}

	comp := compositor.New(hub)
	renderer := terminal.New(display, os.Stdout)
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
	blink := reflexes.NewBlink(comp, store, rng)
	mood := reflexes.NewMoodDrift(store)

	// One tier-shaped scheduler (AD-13) runs both tiers as named jobs on their own
	// cadences: the reflex tier (blink, mood-drift) in-core with no LLM, and the
	// turn tier (the proactive ping) which costs a worker turn. Turn jobs gate
	// inside their own Run (cooldown/budget/battery) and submit through the arbiter
	// — the scheduler loop is unchanged (Yui's condition, Story 3.5).
	sched := scheduler.New()
	sched.Register(scheduler.Job{Name: "blink", NextDelay: blink.NextDelay, Run: blink.Run})
	sched.Register(scheduler.Job{Name: "mood-drift", NextDelay: mood.NextDelay, Run: mood.Run})

	// Proactive-ping turn job (FR4): the pet sometimes messages first, bounded by a
	// minimum-interval cooldown and a daily credit/turn budget so it never spams or
	// overspends (AD-8/NFR14). Conservative tunable config; in-memory budget/cooldown
	// reset on restart. Power is ACPower until the PiSugar2 reader replaces it (Epic 6).
	const (
		proactiveBudgetPerDay = 6
		proactiveCadence      = 30 * time.Minute // how often to consider firing
		proactiveCooldown     = 2 * time.Hour    // minimum interval between pings
	)
	proactiveBudget := turntier.NewBudget(proactiveBudgetPerDay)
	sched.Register(proactive.NewJob(hub, arb, proactiveBudget, turntier.ACPower{}, ownerConvoID,
		func() time.Duration { return proactiveCadence }, proactiveCooldown))

	// Dream-cycle turn job (FR11/AD-15): a scheduled introspective turn that reviews
	// recurring pending learnings, promotes durable ones into the curated tree, and
	// prunes the rest — so a learning the pet keeps re-observing grounds later turns
	// (via the 4.4 read path). It reuses the worker/broker/arbiter exactly like the
	// proactive ping; the worker proposes promote/prune, core (dream.OnResult) writes
	// (AD-6). The sensitive lane stays OFF until Epic 5. Conservative tunable config;
	// the in-memory budget/cooldown reset on restart.
	const (
		dreamBudgetPerDay = 2
		dreamCadence      = 6 * time.Hour  // how often to consider dreaming
		dreamCooldown     = 12 * time.Hour // minimum interval between dreams
	)
	dreamBudget := turntier.NewBudget(dreamBudgetPerDay)
	sched.Register(dream.NewJob(arb, mem, curated, dreamBudget, turntier.ACPower{},
		func() time.Duration { return dreamCadence }, dreamCooldown))

	root := supervisor.New("shelldon")
	// Start order: state-checkpoint first, then dispatch, then transport → reverse
	// drain stops the transport, then dispatch, then state-checkpoint last so its
	// shutdown flush captures the final state after the other edges have stopped.
	root.Add(supervisor.Guard("state-checkpoint", store.RunCheckpointLoop))
	root.Add(supervisor.Guard("core-dispatch", disp.Serve))
	root.Add(supervisor.Guard(transportName, transportServe))
	root.Add(supervisor.Guard("display-terminal", renderer.Serve))
	// Added last so reverse-drain stops it first: the pet stops producing reflex
	// frames before the renderer drains.
	root.Add(supervisor.Guard("reflex-scheduler", sched.Serve))

	// Show an initial face on boot. The mood-driven expression is Story 2.4; the
	// buffered display channel absorbs this push until the renderer starts.
	if err := comp.PushFace(contracts.Face{Expression: contracts.ExpressionNeutral, EyesOpen: true}); err != nil {
		slog.Error("push initial face", "err", err)
	}

	if err := root.Serve(ctx); err != nil {
		slog.Error("supervisor exited with error", "err", err)
		os.Exit(1)
	}
}

// Package proactive is the time-driven LLM proactive-ping behavior (FR4): a
// turn-tier job that, when its cooldown + daily budget + battery gates pass,
// submits a proactive-prompt turn through the arbiter (≤1 in flight, AD-8) and
// publishes the worker's reply as an outbound message — so the pet messages the
// owner first, with no preceding input.
//
// It is a core behavior, not an LLM edge: it composes the turn tier (core/turntier)
// and the bus, submitting through the turntier.Submitter seam and publishing to the
// hub. The worker only proposes; core publishes (AD-6). A failed or empty turn
// publishes nothing — the pet stays quiet rather than spamming errors (AD-8).
package proactive

import (
	"context"
	"time"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/bus"
	"github.com/elliotboney/shelldon_go/core/scheduler"
	"github.com/elliotboney/shelldon_go/core/turntier"
)

// proactivePrompt instructs the worker to generate a self-initiated check-in. The
// monolith worker (Story 3.3) prepends its system persona and runs this as the
// user turn. Tunable story-time config, not a spine invariant.
const proactivePrompt = "Send a brief, warm, in-character check-in message to your owner. One or two sentences."

// NewJob builds the proactive-ping turn job as a scheduler.Job, ready to register
// alongside the reflex jobs. cadence is how often to consider firing; cooldown is
// the minimum interval between pings; budget/power are the shared turn-tier gates;
// ownerConvoID is the conversation the ping targets (the selected transport
// resolves it: CLI ignores it, Telegram maps it to the owner chat). A nil power
// defaults to AC power inside turntier.
func NewJob(hub *bus.Hub, arb turntier.Submitter, budget *turntier.Budget, power turntier.Power, ownerConvoID string, cadence func() time.Duration, cooldown time.Duration) scheduler.Job {
	return turntier.NewJob(turntier.Config{
		Name:     "proactive-ping",
		Cadence:  cadence,
		Cooldown: cooldown,
		Build:    func() contracts.Job { return contracts.Job{Input: proactivePrompt, ConvoID: ownerConvoID} },
		Arbiter:  arb,
		Budget:   budget,
		Power:    power,
		OnResult: func(ctx context.Context, res contracts.Result, err error) {
			if err != nil || res.Reply == "" {
				return // failed or empty proactive turn: stay quiet (AD-8), no ping
			}
			// Core publishes the reply (AD-6); the hub routes by Kind, so the
			// selected transport's outbound consumer delivers it. Dst is cosmetic
			// (as in dispatch.publishReply).
			_ = hub.PublishContext(ctx, contracts.Envelope{
				Header:  contracts.Header{Kind: contracts.KindOutboundMessage, Src: "core", Dst: "owner"},
				Payload: contracts.OutboundMessage{ConvoID: ownerConvoID, Text: res.Reply},
			})
		},
	}).Scheduler()
}

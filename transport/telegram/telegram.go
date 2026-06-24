// Package telegram is the second chat-transport edge actor (AD-12): a bus client
// that long-polls Telegram for owner messages, publishes them as inbound-message
// envelopes, and renders outbound-message envelopes back to the originating chat.
// It speaks only the transport-agnostic message contract in contracts/ — no telego
// type ever crosses into core — and holds its OWN connection credential (the bot
// token), never the broker's model/tool creds (AD-9 scope; NFR8: no cred on the
// bus).
//
// The adapter is run as a supervised edge Service (its Serve method) under the
// suture root (AD-5); a transport failure degrades to reflex-only and never
// crashes core. It mirrors transport/cli, proving the transport seam is pluggable
// with no core change (FR9).
package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/elliotboney/shelldon_go/contracts"
	"github.com/elliotboney/shelldon_go/core/bus"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// NAT-idle watchdog config (tunable story-time config; AD-12 adapter detail). A
// home-router NAT mapping expires after some idle window; a long-poll Timeout
// shorter than that forces a fresh GetUpdates before the mapping dies, keeping the
// long-poll alive. longPollTimeout MUST stay under natIdleWindow.
const (
	longPollTimeout = 30 // seconds: the GetUpdates long-poll timeout
	natIdleWindow   = 60 // seconds: the assumed NAT-idle window the timeout stays under
)

// Adapter-owned environment credential/config (AD-12). The token is the adapter's
// own surface credential — never a broker model/tool cred, never on the bus.
const (
	envTokenKey = "SHELLDON_TELEGRAM_TOKEN"
	envOwnerKey = "SHELLDON_TELEGRAM_OWNER_ID"
)

// Update is the adapter-local inbound message: chat id + text in simple types so
// no telego type leaks past the transport boundary (AD-12). The telego-backed
// Client maps a telego.Update into this.
type Update struct {
	ChatID int64
	Text   string
}

// Client is the narrow Telegram seam the adapter depends on, so tests inject a
// fake without a real bot/network (mirrors broker.Provider / monolith.Completer).
// The telego-backed implementation is the only place telego is touched.
type Client interface {
	// Updates streams inbound messages until ctx is cancelled, which closes the
	// returned channel.
	Updates(ctx context.Context) (<-chan Update, error)
	// Send delivers text to the given chat.
	Send(ctx context.Context, chatID int64, text string) error
}

// Adapter bridges a Telegram long-poll to the bus.
type Adapter struct {
	hub      *bus.Hub
	outbound <-chan contracts.Envelope
	c        Client
	ownerID  int64 // 0 = accept any chat; non-zero restricts inbound to that chat
}

// New returns a Telegram adapter over the given client. ownerID restricts inbound
// to a single chat when non-zero (0 = accept any chat).
func New(hub *bus.Hub, outbound <-chan contracts.Envelope, c Client, ownerID int64) *Adapter {
	return &Adapter{hub: hub, outbound: outbound, c: c, ownerID: ownerID}
}

// NewFromEnv builds an Adapter backed by a real telego bot, resolving the bot
// token (the adapter's own credential, AD-12) and the optional owner id from the
// environment. A missing token is an error so the supervised edge degrades to
// reflex-only (AD-5) rather than running tokenless. The token value is never
// logged.
func NewFromEnv(hub *bus.Hub, outbound <-chan contracts.Envelope) (*Adapter, error) {
	token := os.Getenv(envTokenKey)
	if token == "" {
		return nil, fmt.Errorf("telegram: %s not set", envTokenKey)
	}
	bot, err := telego.NewBot(token, telego.WithDefaultLogger(false, true))
	if err != nil {
		return nil, fmt.Errorf("telegram: new bot: %w", err)
	}
	var ownerID int64
	if v := os.Getenv(envOwnerKey); v != "" {
		ownerID, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("telegram: invalid %s: %w", envOwnerKey, err)
		}
	}
	return New(hub, outbound, &telegoClient{bot: bot}, ownerID), nil
}

// Serve streams inbound updates (via a background read loop) and renders outbound
// replies until ctx is cancelled. Cancelling ctx closes the telego updates channel
// (clean shutdown). It returns ctx.Err() on shutdown.
func (a *Adapter) Serve(ctx context.Context) error {
	updates, err := a.c.Updates(ctx)
	if err != nil {
		return fmt.Errorf("telegram: start updates: %w", err)
	}
	go a.readLoop(ctx, updates)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case env := <-a.outbound:
			msg, ok := env.Payload.(contracts.OutboundMessage)
			if !ok {
				continue // defensive: wrong payload for this kind
			}
			chatID, err := convoToChatID(msg.ConvoID)
			if err != nil {
				slog.Warn("telegram: undeliverable reply, bad convo id", "convo_id", msg.ConvoID, "err", err)
				continue
			}
			if err := a.c.Send(ctx, chatID, msg.Text); err != nil {
				slog.Warn("telegram: send failed", "chat_id", chatID, "err", err)
			}
		}
	}
}

// readLoop maps each inbound Update to an inbound-message envelope and publishes
// it. It returns when updates is closed (ctx cancelled) or ctx is done. The native
// chat id is mapped to core's ConvoID at the edge (AD-12); a telego type never
// crosses into core.
func (a *Adapter) readLoop(ctx context.Context, updates <-chan Update) {
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-updates:
			if !ok {
				return
			}
			if a.ownerID != 0 && u.ChatID != a.ownerID {
				continue // single-owner guard: drop a non-owner chat
			}
			_ = a.hub.PublishContext(ctx, contracts.Envelope{
				Header:  contracts.Header{Kind: contracts.KindInboundMessage, Src: "telegram", Dst: "core"},
				Payload: contracts.InboundMessage{ConvoID: chatIDToConvo(u.ChatID), Text: u.Text},
			})
		}
	}
}

// chatIDToConvo maps a native Telegram chat id into core's ConvoID at the edge.
func chatIDToConvo(chatID int64) string { return strconv.FormatInt(chatID, 10) }

// convoToChatID reverses the edge mapping for an outbound reply.
func convoToChatID(convoID string) (int64, error) { return strconv.ParseInt(convoID, 10, 64) }

// telegoClient is the telego-backed Client — the only place telego is touched.
type telegoClient struct {
	bot *telego.Bot
}

// Updates starts the long-poll with the NAT-watchdog timeout and adapts each
// text-bearing telego.Update into the adapter-local Update type. Non-text and
// non-message updates are skipped.
func (t *telegoClient) Updates(ctx context.Context) (<-chan Update, error) {
	params := (&telego.GetUpdatesParams{}).WithTimeout(longPollTimeout)
	raw, err := t.bot.UpdatesViaLongPolling(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make(chan Update)
	go func() {
		defer close(out)
		for u := range raw {
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			select {
			case out <- Update{ChatID: u.Message.Chat.ID, Text: u.Message.Text}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Send delivers text to chatID via the Telegram Bot API.
func (t *telegoClient) Send(ctx context.Context, chatID int64, text string) error {
	_, err := t.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), text))
	return err
}

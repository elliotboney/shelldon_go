package contracts

// InboundMessage and OutboundMessage are the transport-agnostic message contract
// (AD-12): every chat transport adapter (CLI now, Telegram in Story 3.4) maps its
// native input into an InboundMessage and renders an OutboundMessage. Adapter-
// native types (e.g. a telego.Update) never cross this boundary into core — core
// sees only these structs.
//
// ConvoID is core's conversation-identity field (AD-12); the adapter maps its
// native conversation id into it at the edge. The CLI maps its single
// conversation to a fixed id.

// InboundMessage is a chat message arriving from a transport adapter.
type InboundMessage struct {
	ConvoID string // conversation this message belongs to
	Text    string // the message text
}

func (InboundMessage) isPayload() {}

// OutboundMessage is a reply core sends to a transport adapter for rendering.
type OutboundMessage struct {
	ConvoID string // conversation this reply belongs to
	Text    string // the reply text
}

func (OutboundMessage) isPayload() {}

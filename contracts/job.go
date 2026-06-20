package contracts

// Job is what core dispatches to the worker: the input for a single turn. Fields
// are kept minimal and honest to what M0 needs. A Job never carries a credential
// (NFR8); the broker injects creds internally at egress (Story 3.1).
type Job struct {
	Input   string // the user's turn input text
	ConvoID string // conversation this turn belongs to, for routing context
}

func (Job) isPayload() {}

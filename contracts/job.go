package contracts

// Job is what core dispatches to the worker: the input for a single turn. Fields
// are kept minimal and honest to what M0 needs. A Job never carries a credential
// (NFR8); the broker injects creds internally at egress (Story 3.1).
type Job struct {
	Input   string // the user's turn input text
	ConvoID string // conversation this turn belongs to, for routing context
	Kind    string // turn kind: "" (JobReply) = normal reply turn; JobDream = introspective dream turn (Story 4.5)
}

// Job kinds tell the worker which flow to run. The zero value (JobReply) is a
// normal reply turn — back-compatible with every Job built before Story 4.5
// (additive field, NFR9/AD-10). A JobDream job tells the worker to run the
// introspective dream flow (review candidate learnings, propose promote/prune)
// instead of replying (AD-15).
const (
	JobReply = ""      // normal reply turn (default)
	JobDream = "dream" // introspective dream turn
)

func (Job) isPayload() {}

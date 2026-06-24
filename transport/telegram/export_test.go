package telegram

// Exported for the black-box NAT-watchdog invariant test (AC3): the long-poll
// timeout must stay under the NAT-idle window. Mirrors broker/export_test.go.
const (
	LongPollTimeout = longPollTimeout
	NATIdleWindow   = natIdleWindow
)

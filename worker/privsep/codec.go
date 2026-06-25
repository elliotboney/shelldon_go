// Package privsep is the M3 Privsep-lite implementation of the AD-2 Worker seam:
// the untrusted brain runs as a uid-separated recycled subprocess (re-exec of the
// binary, not fork — AD-2), and the bus transport beneath the seam swaps from
// in-process channels to length-prefixed encoding/gob over a Unix-domain socket
// (AD-4). The swap is invisible above the seam: *privsep.Worker satisfies
// worker.Worker exactly as the Monolith+ goroutine worker does, so the arbiter,
// dispatch, and scheduler are unchanged.
//
// This package holds NO model/tool credentials and imports no broker/LLM SDK
// (AD-9/AD-1): the parent only launches the child and frames Job→Result over the
// wire; the inner worker the child runs is injected by main via ChildMain. The
// seam carries bare Job/Result (the arbiter owns turn identity above it, AD-11),
// not full Envelopes.
package privsep

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"

	"github.com/elliotboney/shelldon_go/contracts"
)

// Wire framing constants. Each frame is a 4-byte big-endian length prefix
// followed by that many bytes of a self-contained gob stream (AD-4: "UDS frames
// are length-prefixed encoding/gob"). A fresh gob stream per frame keeps frames
// independent — a torn-down/recycled child never inherits half a type table.
const (
	frameLenBytes = 4       // uint32 big-endian length prefix
	maxFrameBytes = 1 << 20 // 1 MiB cap: a turn payload is small; a corrupt length must never drive an unbounded alloc
)

// jobFrame is the parent→child wire payload: a single turn input. Wrapped (rather
// than encoding contracts.Job bare) so additive header fields can ride the wall
// later without reshaping callers (AD-4 additive-only).
type jobFrame struct {
	Job contracts.Job
}

// resultFrame is the child→parent wire payload: the worker's proposed Result plus
// a flattened inner-error string. gob cannot carry a Go error value, so the child
// stringifies any AssembleAndPropose error into Err (empty on success) and the
// parent re-wraps it — preserving the seam's (Result, error) contract so the
// arbiter degrades to a reflex ack on failure exactly as with the goroutine worker
// (AD-8).
type resultFrame struct {
	Result contracts.Result
	Err    string
}

// encodeFrame writes v as one length-prefixed gob frame to w. It buffers the gob
// bytes first so the length prefix is exact, and rejects an over-cap payload
// before writing anything.
func encodeFrame(w io.Writer, v any) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return fmt.Errorf("privsep: gob encode: %w", err)
	}
	if buf.Len() > maxFrameBytes {
		return fmt.Errorf("privsep: outbound frame too large: %d bytes (max %d)", buf.Len(), maxFrameBytes)
	}
	var prefix [frameLenBytes]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(buf.Len()))
	if _, err := w.Write(prefix[:]); err != nil {
		return fmt.Errorf("privsep: write length prefix: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("privsep: write frame body: %w", err)
	}
	return nil
}

// decodeFrame reads one length-prefixed gob frame from r into v. A clean EOF on
// the length prefix (the peer closed the connection) is returned unwrapped so
// callers can detect orderly shutdown with errors.Is(err, io.EOF). An oversized
// length is rejected before allocating the body, so a corrupt/hostile prefix
// cannot exhaust memory.
func decodeFrame(r io.Reader, v any) error {
	var prefix [frameLenBytes]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err // EOF / ErrClosed propagate unwrapped for shutdown detection
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n > maxFrameBytes {
		return fmt.Errorf("privsep: inbound frame too large: %d bytes (max %d)", n, maxFrameBytes)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return fmt.Errorf("privsep: read frame body: %w", err)
	}
	if err := gob.NewDecoder(bytes.NewReader(body)).Decode(v); err != nil {
		return fmt.Errorf("privsep: gob decode: %w", err)
	}
	return nil
}

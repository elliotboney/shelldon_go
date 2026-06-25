package privsep

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/elliotboney/shelldon_go/contracts"
)

// TestCodec_RoundTrip proves a Job and a Result (including MemoryOps and a
// flattened error) survive a length-prefixed gob frame unchanged — the wire
// equivalent of the M0 contract round-trip (AC1, AD-4/NFR9).
func TestCodec_RoundTrip(t *testing.T) {
	t.Run("job frame", func(t *testing.T) {
		want := jobFrame{Job: contracts.Job{Input: "hello", ConvoID: "c1", Kind: contracts.JobDream}}
		var buf bytes.Buffer
		if err := encodeFrame(&buf, want); err != nil {
			t.Fatalf("encode: %v", err)
		}
		var got jobFrame
		if err := decodeFrame(&buf, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("round-trip mismatch\n got: %#v\nwant: %#v", got, want)
		}
	})

	t.Run("result frame with ops and error", func(t *testing.T) {
		want := resultFrame{
			Result: contracts.Result{
				Reply: "hi",
				MemoryOps: []contracts.MemoryOp{
					{Kind: contracts.MemoryOpPromoteLearning, PatternKey: "k", Observation: "o"},
				},
			},
			Err: "broker exhausted",
		}
		var buf bytes.Buffer
		if err := encodeFrame(&buf, want); err != nil {
			t.Fatalf("encode: %v", err)
		}
		var got resultFrame
		if err := decodeFrame(&buf, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("round-trip mismatch\n got: %#v\nwant: %#v", got, want)
		}
	})
}

// TestDecodeFrame_RejectsOversize proves a corrupt/hostile length prefix above
// the cap is rejected before the body is allocated — no unbounded alloc.
func TestDecodeFrame_RejectsOversize(t *testing.T) {
	var buf bytes.Buffer
	var prefix [frameLenBytes]byte
	binary.BigEndian.PutUint32(prefix[:], maxFrameBytes+1)
	buf.Write(prefix[:])

	var jf jobFrame
	if err := decodeFrame(&buf, &jf); err == nil {
		t.Fatal("expected oversize frame to be rejected")
	}
}

// TestDecodeFrame_ShortBodyErrors proves a truncated body errors rather than
// hangs (io.ReadFull → ErrUnexpectedEOF).
func TestDecodeFrame_ShortBodyErrors(t *testing.T) {
	var buf bytes.Buffer
	var prefix [frameLenBytes]byte
	binary.BigEndian.PutUint32(prefix[:], 100) // claim 100 bytes...
	buf.Write(prefix[:])
	buf.Write([]byte{1, 2, 3}) // ...but supply 3

	var jf jobFrame
	if err := decodeFrame(&buf, &jf); err == nil {
		t.Fatal("expected short-body read to error")
	}
}

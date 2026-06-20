package contracts

// MemoryOp is a typed placeholder for a memory mutation the worker proposes. The
// worker never writes directly; this is the proposal channel (AD-6). The concrete
// op vocabulary arrives with Epic 4.
type MemoryOp struct {
	Kind string // proposed op kind; vocabulary defined later
}

// Result is what the worker proposes back to core: the reply text plus any
// proposed memory operations. The worker proposes; core decides and writes.
type Result struct {
	Reply     string     // proposed reply text
	MemoryOps []MemoryOp // proposed memory mutations (never applied by the worker)
}

func (Result) isPayload() {}

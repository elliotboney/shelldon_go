// Package memory is the seed of shelldon's memory layer (AD-7). For M0 it holds
// only the atomic markdown-write primitive and its required crash-safety test;
// Epic 4 builds the sqlite store + curated markdown tree + DIRECTIVE.md on top.
//
// Nothing in M0 calls WriteAtomic yet — it exists for the required M0 atomic-write
// crash-safety test (AD-10) and as the Epic 4 foundation.
package memory

import (
	"os"

	"github.com/google/renameio/v2"
)

// WriteAtomic writes data to path atomically (AD-7/NFR11): a reader sees either
// the prior file or the fully-written new file, never a partial/torn write.
// renameio writes a temp file in the same directory, fsyncs it, then renames over
// path — the rename is the atomic commit point, so a crash before it leaves the
// prior file intact. (The explicit parent-directory fsync for power-loss
// durability beyond atomicity is AD-7's add, deferred; atomicity alone satisfies
// the M0 crash-safety test.)
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	return renameio.WriteFile(path, data, perm)
}

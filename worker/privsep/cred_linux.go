//go:build linux

package privsep

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

// applyCredential drops the child to the configured uid/gid via SysProcAttr.
// Gated three ways: a uid must be configured (non-zero), and the parent must be
// root (only root may set child credentials). Any gate failing falls back to
// same-uid with a logged notice (AD-17) — the transport still works; the
// OS-enforced isolation proof is Story 5.2 on the Pi.
func applyCredential(cmd *exec.Cmd, uid, gid int) {
	if uid == 0 {
		return // not configured: run same-uid silently (the dev/default case)
	}
	if os.Geteuid() != 0 {
		logSameUID("parent not root", uid)
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	slog.Info("privsep: child will run uid-separated", "uid", uid, "gid", gid)
}

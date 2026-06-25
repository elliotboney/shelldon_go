//go:build !linux

package privsep

import "os/exec"

// applyCredential is a no-op off Linux: uid-drop is not supported (darwin dev).
// A configured uid logs a same-uid notice (AD-17); the transport is unaffected,
// so the round-trip proof runs on the laptop and the real drop lands on the Pi.
func applyCredential(_ *exec.Cmd, uid, gid int) {
	if uid != 0 {
		logSameUID("uid-drop unsupported on this platform", uid)
	}
	_ = gid
}

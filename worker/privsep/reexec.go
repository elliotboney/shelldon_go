package privsep

import (
	"os"
	"runtime"
)

// execPath resolves the binary to re-exec for the child. On Linux it is
// /proc/self/exe — the canonical privsep idiom (AD-2), stable even if the binary
// on disk is replaced (atomic deploy swap) while running. Elsewhere (darwin dev)
// it falls back to os.Executable().
func execPath() (string, error) {
	if runtime.GOOS == "linux" {
		return "/proc/self/exe", nil
	}
	return os.Executable()
}

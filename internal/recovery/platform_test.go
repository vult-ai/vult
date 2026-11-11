package recovery

import "runtime"

// On macOS an APFS clone keeps links as links; the Linux copy refuses them.
func isLinux() bool { return runtime.GOOS == "linux" }

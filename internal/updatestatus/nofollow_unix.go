//go:build darwin || linux

package updatestatus

import "syscall"

const syscallNoFollow = syscall.O_NOFOLLOW

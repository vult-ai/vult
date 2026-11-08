//go:build !darwin && !linux

package fssecurity

import "os"

// POSIX ACL permissions are bounded by the group mask, already required zero.
func NoExtendedACL(*os.File) bool { return true }

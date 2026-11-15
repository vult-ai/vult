//go:build !vult_updates || !(darwin || linux)

package main

// refreshBackups has nothing to record without an update installer.
func refreshBackups() {}

func backupLine() string { return "" }

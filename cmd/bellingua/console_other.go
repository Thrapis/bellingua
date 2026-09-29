//go:build !windows

package main

// ownsConsole is Windows-only: elsewhere a terminal outlives the process.
func ownsConsole() bool { return false }

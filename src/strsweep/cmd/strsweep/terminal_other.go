//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package main

import "os"

// Other platforms retain the complete progress table without live terminal output.
func isTerminal(f *os.File) bool { return false }

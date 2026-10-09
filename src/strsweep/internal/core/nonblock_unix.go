//go:build unix

package core

import "syscall"

func nonblockFlag() int { return syscall.O_NONBLOCK }

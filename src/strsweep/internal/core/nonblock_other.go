//go:build !unix

package core

func nonblockFlag() int { return 0 }

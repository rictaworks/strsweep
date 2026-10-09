//go:build !linux

package core

import (
	"fmt"
	"io/fs"
	"os"
)

func recoveryRename(*os.Root, string, *os.Root, string) error {
	return fmt.Errorf("recovery requires Linux")
}
func recoveryLink(*os.Root, string, *os.Root, string) error {
	return fmt.Errorf("recovery requires Linux")
}

func privateRecoveryDirectory(fs.FileInfo) bool { return false }

//go:build unix

package transport

import (
	"os"

	"syscall"
)

func openStaticFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

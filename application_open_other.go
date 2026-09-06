//go:build !unix

package transport

import "os"

func openStaticFile(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}

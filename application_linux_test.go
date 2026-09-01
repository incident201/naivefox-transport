//go:build linux

package transport

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestApplicationFIFOIsRejectedWithoutBlockingProvision(t *testing.T) {
	root := copyApplicationTemplate(t)
	path := filepath.Join(root, "assets", "site.css")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := loadApplication(root)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO validation result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("application loader blocked while opening a FIFO")
	}
}

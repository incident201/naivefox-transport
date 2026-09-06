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

func TestApplicationStaticSpecialFilesDoNotBlock(t *testing.T) {
	root := copyApplicationTemplate(t)
	if err := syscall.Mkfifo(filepath.Join(root, "extra.fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	module := provisionApplicationSite(t, root)
	done := make(chan int, 1)
	go func() {
		done <- requestApplicationSite(t, module, "GET", "/extra.fifo", nil).Code
	}()
	select {
	case status := <-done:
		if status != 404 {
			t.Fatalf("FIFO response: %d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("static handler blocked on FIFO")
	}
	// The nonblocking open also handles a regular file replaced by a FIFO
	// after the handler's initial Stat, without hanging inside Open.
	opened := make(chan error, 1)
	go func() {
		file, err := openStaticFile(module.application.directory, "extra.fifo")
		if err == nil {
			file.Close()
		}
		opened <- err
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("static open blocks on a replaced FIFO")
	}
}

func applicationRootHandles(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		target, _ := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if target == root {
			count++
		}
	}
	return count
}

func TestApplicationDirectoryLifetime(t *testing.T) {
	root := copyApplicationTemplate(t)
	baseline := applicationRootHandles(t, root)
	for range 5 {
		module := &Transport{ApplicationRoot: root}
		if err := module.Provision(testCaddyContext(t)); err == nil {
			module.Cleanup()
			t.Fatal("missing forward proxy accepted")
		}
		if applicationRootHandles(t, root) != baseline {
			t.Fatal("failed proxy provisioning leaked the application root")
		}
	}
	module := &Transport{ApplicationRoot: root, ForwardProxy: testForwardProxy(), StatsPath: t.TempDir()}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	if applicationRootHandles(t, root) != baseline+1 {
		t.Error("successful provisioning should retain exactly one root")
	}
	if err := module.Cleanup(); err == nil {
		t.Fatal("stats write to a directory should fail")
	}
	if applicationRootHandles(t, root) != baseline {
		t.Fatal("cleanup failed to close the root after a stats write error")
	}
	if _, err := module.application.directory.Stat("index.html"); err == nil {
		t.Fatal("root still usable after cleanup")
	}
	writeApplicationFile(t, root, "assets/site.css", []byte{0xff})
	for range 5 {
		if application, err := loadApplication(root); err == nil {
			application.close()
			t.Fatal("invalid application accepted")
		}
		if applicationRootHandles(t, root) != baseline {
			t.Fatal("failed application load leaked its root")
		}
	}
}

func TestApplicationRootReplacementRequiresReload(t *testing.T) {
	root := copyApplicationTemplate(t)
	writeApplicationFile(t, root, "version.txt", []byte("old site"))
	oldModule := provisionApplicationSite(t, root)
	// Both destinations are inside directories managed by this test.
	moved := filepath.Join(t.TempDir(), "old-site")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	replacement := copyApplicationTemplate(t)
	writeApplicationFile(t, replacement, "version.txt", []byte("new site"))
	if err := os.Rename(replacement, root); err != nil {
		t.Fatal(err)
	}
	response := requestApplicationSite(t, oldModule, "GET", "/version.txt", nil)
	if response.Code != 200 || response.Body.String() != "old site" {
		t.Fatal("live module silently switched to a replacement directory")
	}
	newModule := provisionApplicationSite(t, root)
	response = requestApplicationSite(t, newModule, "GET", "/version.txt", nil)
	if response.Code != 200 || response.Body.String() != "new site" {
		t.Fatal("reload failed to use the replacement directory")
	}
}

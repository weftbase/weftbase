//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/discovery"
)

// TestServerStopsOnSIGHUP (review finding 5): `weftbase dev` stops
// gracefully on SIGHUP (the terminal closing) like SIGINT and SIGTERM,
// so its discovery file is removed.
func TestServerStopsOnSIGHUP(t *testing.T) {
	skipWithoutSelfSignal(t)
	dir := discoveryDir(t)
	base := freeBase(t, 1)
	var out syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"dev", "--addr", loop(base), "--data", t.TempDir()}, &out, &out)
	}()
	waitServing(t, loop(base), done, &out)
	if _, err := os.Stat(filepath.Join(dir, discovery.FileName)); err != nil {
		t.Fatalf("no discovery file while serving: %v", err)
	}
	signalSelf(t, syscall.SIGHUP)
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("dev after SIGHUP: exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve never returned after SIGHUP")
	}
	if _, err := os.Stat(filepath.Join(dir, discovery.FileName)); !os.IsNotExist(err) {
		t.Errorf("the discovery file outlived a SIGHUP: %v", err)
	}
	notListening(t, loop(base))
}

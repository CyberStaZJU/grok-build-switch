//go:build wailsgui && darwin

package main

import (
	"context"
	"testing"
)

func TestDarwinCloseExitsWithoutTrayRestoreEntry(t *testing.T) {
	controller := newGUITrayController("http://127.0.0.1:17878", nil)
	controller.register()
	controller.startup(context.Background())
	if controller.beforeClose(context.Background()) {
		t.Fatal("Darwin close must not be intercepted when no restore entry exists")
	}
	controller.shutdown()
}

//go:build wailsgui && darwin

package main

import "context"

// guiTrayController intentionally does not start fyne systray on macOS. Wails
// owns Cocoa's main loop, and without a tray restore entry closing must exit.
type guiTrayController struct{}

func newGUITrayController(_ string, _ []byte) *guiTrayController {
	return &guiTrayController{}
}

func (*guiTrayController) register() {}

func (*guiTrayController) startup(context.Context) {}

func (*guiTrayController) beforeClose(context.Context) bool { return false }

func (*guiTrayController) shutdown() {}

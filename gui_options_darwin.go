//go:build wailsgui && darwin

package main

import "github.com/wailsapp/wails/v2/pkg/options"

// configurePlatformOptions is a no-op on macOS. Menu-bar resident behavior
// is achieved via HideWindowOnClose + beforeClose in the Wails app config.
func configurePlatformOptions(_ *options.App, _ string) {}

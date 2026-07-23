//go:build wailsgui && windows

package main

import (
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

func configurePlatformOptions(app *options.App, dataDir string) {
	app.Windows = &windows.Options{
		Theme:               windows.Light,
		WebviewUserDataPath: filepath.Join(dataDir, "wails-webview2"),
		ResizeDebounceMS:    12,
		Messages: &windows.Messages{
			Webview2NotInstalled: "需要 Microsoft Edge WebView2 Runtime 才能运行 Grok Build Switch。",
			Error:                "Grok Build Switch 启动失败",
		},
	}
}

//go:build wailsgui

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"grok_switch/internal/browseruse"
	"grok_switch/internal/cliproxy"
	"grok_switch/internal/collaboration"
	"grok_switch/internal/crash"
	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/remoteaccess"
	"grok_switch/internal/routing"
	"grok_switch/internal/server"
	"grok_switch/internal/settings"
	"grok_switch/internal/singleinstance"
	"grok_switch/internal/ssh"
	"grok_switch/internal/switcher"
)

func main() {
	defer crash.RecoverMainThread()
	if len(os.Args) > 1 && os.Args[1] == "browser-use-mcp" {
		if err := browseruse.New().Serve(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	resolved, err := paths.Resolve()
	if err != nil {
		guiFatal(err)
	}
	crash.Setup(resolved.LogFile)
	if err := resolved.Ensure(); err != nil {
		guiFatal(err)
	}

	settingsStore := settings.NewStore(resolved.SettingsFile)
	instanceLock, alreadyRunning, err := singleinstance.Acquire(resolved.DataDir)
	if err != nil {
		guiFatal(fmt.Errorf("创建单实例锁失败: %w", err))
	}
	if alreadyRunning {
		url, findErr := waitForExistingInstanceURL(settingsStore, resolved.DataDir, 3*time.Second)
		if findErr != nil {
			guiFatal(fmt.Errorf("连接正在运行的 grok_switch 失败: %w", findErr))
		}
		if err := runWailsWindow(url, resolved.DataDir); err != nil {
			guiFatal(err)
		}
		return
	}
	defer instanceLock.Close()

	exePath, err := os.Executable()
	if err != nil {
		guiFatal(err)
	}
	exePath, _ = filepath.Abs(exePath)

	profileStore := profiles.NewStore(resolved.ProfilesFile)
	sw := &switcher.Switcher{
		ConfigPath: resolved.GrokConfig,
		Profiles:   profileStore,
	}
	if err := sw.EnsureDefaultProfile(); err != nil {
		crash.Logf("default profile import skipped: %v", err)
	}
	routingStore := routing.NewStore(resolved.RoutingFile)
	collaborationStore := collaboration.NewStore(resolved.CollaborationFile)
	routingSnapshot, err := routingStore.Initialize(profileStore)
	if err != nil {
		guiFatal(err)
	}
	profileList, err := profileStore.List()
	if err != nil {
		guiFatal(err)
	}
	hydratedRouting, err := routing.ProjectWithSnapshot(profileList, routingSnapshot)
	if err != nil {
		guiFatal(err)
	}
	// Schema v1 allowed non-Responses web_search routes. Repair that legacy
	// optional field during startup migration so an upgrade cannot prevent the
	// local service from launching; interactive updates remain strictly rejected.
	hydratedRouting, _ = routing.RepairUnsupportedWebSearch(hydratedRouting)
	if err := sw.ApplyRouting(hydratedRouting); err != nil {
		guiFatal(fmt.Errorf("应用启动路由配置失败: %w", err))
	}
	if !routing.PersistedEqual(hydratedRouting, routingSnapshot) {
		if _, err := routingStore.Replace(hydratedRouting); err != nil {
			guiFatal(fmt.Errorf("修复启动路由策略失败: %w", err))
		}
	}
	currentSettings, err := settingsStore.Get()
	if err != nil {
		guiFatal(err)
	}
	home, _ := os.UserHomeDir()
	proxyManager := cliproxy.NewManager(resolved.DataDir, home, cliproxy.ResolveBuiltinBinary(exePath), cliproxy.DarwinKeychain{})
	sshHandler := ssh.NewHandler(resolved.DataDir)

	appServer := &server.Server{
		Paths:             resolved,
		Profiles:          profileStore,
		Routing:           routingStore,
		Collaboration:     collaborationStore,
		Settings:          settingsStore,
		RemoteAccess:      remoteaccess.NewStore(resolved.RemoteAccessFile),
		Switcher:          sw,
		SubscriptionProxy: proxyManager,
		SSH:               sshHandler,
		BrowserOpener:     server.SafeBrowserOpener{},
		Assets:            assets,
		ExePath:           exePath,
	}
	httpServer, port, err := appServer.Listen(currentSettings.Port)
	if err != nil {
		guiFatal(err)
	}
	if err := appServer.EnsureSubscriptionProxyRoutes(); err != nil {
		_ = httpServer.Shutdown(context.Background())
		guiFatal(fmt.Errorf("更新订阅代理路由失败: %w", err))
	}
	if err := appServer.ApplyCurrentRouting(); err != nil {
		_ = httpServer.Shutdown(context.Background())
		guiFatal(fmt.Errorf("最终应用组合路由失败: %w", err))
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := runWailsWindow(url, resolved.DataDir); err != nil {
		guiFatal(err)
	}
}

func runWailsWindow(url, dataDir string) error {
	loadingAssets, err := fs.Sub(assets, "gui")
	if err != nil {
		return fmt.Errorf("加载 GUI 启动画面失败: %w", err)
	}
	targetJSON, _ := json.Marshal(url)
	redirectScript := fmt.Sprintf(`if (window.location.origin !== new URL(%s).origin) { window.location.replace(%s); }`, targetJSON, targetJSON)
	icon, _ := assets.ReadFile("assets/icon.ico")
	trayController := newGUITrayController(url, icon)
	trayController.register()
	defer trayController.shutdown()

	appOptions := &options.App{
		Title:             "Grok Build Switch",
		Width:             1280,
		Height:            820,
		MinWidth:          960,
		MinHeight:         640,
		BackgroundColour:  &options.RGBA{R: 247, G: 247, B: 245, A: 255},
		AssetServer:       &assetserver.Options{Assets: loadingAssets},
		OnStartup:         trayController.startup,
		OnDomReady:        func(ctx context.Context) { wailsruntime.WindowExecJS(ctx, redirectScript) },
		OnBeforeClose:     trayController.beforeClose,
		OnShutdown:        func(context.Context) { trayController.shutdown() },
		WindowStartState:  options.Normal,
		HideWindowOnClose: true,
	}
	configurePlatformOptions(appOptions, dataDir)
	return wails.Run(appOptions)
}

func guiFatal(err error) {
	crash.ReportFatal(err)
	os.Exit(1)
}

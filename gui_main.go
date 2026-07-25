//go:build wailsgui

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"grok_switch/internal/agentbridge"
	"grok_switch/internal/cliproxy"
	"grok_switch/internal/cpamint"
	"grok_switch/internal/crash"
	"grok_switch/internal/grokauth"
	"grok_switch/internal/grokpool"
	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/registrar"
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

	guiSettings, err := settingsStore.Get()
	if err != nil {
		guiFatal(err)
	}
	oauthClientID := guiSettings.OAuthClientID

	profileStore := profiles.NewStore(resolved.ProfilesFile)
	grokAuthStore := grokauth.NewStore(resolved.GrokAuthFile)
	grokAuthStore.SetClientID(oauthClientID)
	grokPool, err := grokpool.NewManager(resolved.GrokPoolDir, oauthClientID)
	if err != nil {
		guiFatal(err)
	}
	if err := grokAuthStore.SetProxyURL(grokPool.Status().Settings.ProxyURL); err != nil {
		guiFatal(err)
	}
	if singleStatus, statusErr := grokAuthStore.Status(); statusErr == nil && singleStatus.Configured {
		if raw, readErr := os.ReadFile(resolved.GrokAuthFile); readErr == nil {
			if _, migrateErr := grokPool.Ensure([]grokpool.ImportFile{{Name: "legacy-grok-auth.json", Content: string(raw)}}); migrateErr != nil {
				crash.Logf("migrate legacy Grok auth into pool: %v", migrateErr)
			}
		}
	}
	grokPool.Start()
	defer grokPool.Close()
	registrarService, err := registrar.NewService(resolved.DataDir)
	if err != nil {
		guiFatal(err)
	}
	defer registrarService.Close()
	registrarService.SetClientID(oauthClientID)

	sw := &switcher.Switcher{
		ConfigPath: resolved.GrokConfig,
		BackupsDir: resolved.BackupsDir,
		Profiles:   profileStore,
	}
	if err := sw.EnsureDefaultProfile(); err != nil {
		crash.Logf("default profile import skipped: %v", err)
	}
	routingStore := routing.NewStore(resolved.RoutingFile)
	routingSnapshot, err := routingStore.Initialize(profileStore)
	if err != nil {
		guiFatal(err)
	}
	profileList, err := profileStore.List()
	if err != nil {
		guiFatal(err)
	}
	startupPolicy := routing.RepairPolicy(profileList, routingSnapshot.Policy)
	hydratedRouting, err := routing.ProjectWithPolicy(profileList, startupPolicy)
	if err != nil {
		guiFatal(err)
	}
	if err := sw.ApplyRouting(hydratedRouting); err != nil {
		guiFatal(fmt.Errorf("应用启动路由配置失败: %w", err))
	}
	if startupPolicy != routingSnapshot.Policy {
		if _, err := routingStore.UpdatePolicy(startupPolicy); err != nil {
			guiFatal(fmt.Errorf("修复启动路由策略失败: %w", err))
		}
	}
	currentSettings, err := settingsStore.Get()
	if err != nil {
		guiFatal(err)
	}
	agent := agentbridge.New(resolved.GrokHome, filepath.Join(resolved.DataDir, "agent.log"))
	agent.SetDefaultCwd(currentSettings.AgentDefaultCwd)
	defer agent.Stop()
	home, _ := os.UserHomeDir()
	proxyManager := cliproxy.NewManager(resolved.DataDir, home, cliproxy.ResolveBuiltinBinary(exePath), cliproxy.DarwinKeychain{})
	sshHandler := ssh.NewHandler(resolved.DataDir)

	appServer := &server.Server{
		Paths:             resolved,
		Profiles:          profileStore,
		Routing:           routingStore,
		Settings:          settingsStore,
		RemoteAccess:      remoteaccess.NewStore(resolved.RemoteAccessFile),
		GrokAuth:          grokAuthStore,
		GrokPool:          grokPool,
		CpaMint:           cpamint.NewService(oauthClientID),
		Registrar:         registrarService,
		Switcher:          sw,
		Agent:             agent,
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
	if err := appServer.EnsureCodeBuddyProfile(); err != nil {
		crash.Logf("CodeBuddy managed profile skipped: %v", err)
	}
	if err := appServer.ApplyCurrentRouting(); err != nil {
		_ = httpServer.Shutdown(context.Background())
		guiFatal(fmt.Errorf("最终应用组合路由失败: %w", err))
	}
	if crashFile := resolved.LogFile; crashFile != "" {
		if f, ferr := os.OpenFile(crashFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr == nil {
			httpServer.ErrorLog = log.New(f, "http: ", log.LstdFlags)
			defer f.Close()
		}
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

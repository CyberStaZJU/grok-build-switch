//go:build !wailsgui

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"grok_switch/internal/autostart"
	"grok_switch/internal/browseruse"
	"grok_switch/internal/cliproxy"
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
	"grok_switch/internal/tray"
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

	silent := flag.Bool("silent", false, "start without opening browser")
	noTray := flag.Bool("no-tray", false, "run http server without tray")
	flag.Parse()

	resolved, err := paths.Resolve()
	if err != nil {
		fatal(err)
	}
	// Initialize diagnostics before any directory setup so permission failures
	// still produce a native error dialog and use the log whenever possible.
	crash.Setup(resolved.LogFile)
	if err := resolved.Ensure(); err != nil {
		fatal(err)
	}

	settingsStore := settings.NewStore(resolved.SettingsFile)
	instanceLock, alreadyRunning, err := singleinstance.Acquire(resolved.DataDir)
	if err != nil {
		fatal(fmt.Errorf("创建单实例锁失败: %w", err))
	}
	if alreadyRunning {
		url, findErr := waitForExistingInstanceURL(settingsStore, resolved.DataDir, 3*time.Second)
		if findErr == nil {
			if openErr := tray.OpenBrowser(url); openErr == nil {
				return
			} else {
				crash.Logf("open existing instance failed: %v", openErr)
			}
		} else {
			crash.Logf("find existing instance failed: %v", findErr)
		}
		crash.ShowInfo("grok_switch", "grok_switch 已经在运行，但未能自动打开管理页面。请使用系统托盘图标打开。")
		return
	}
	defer instanceLock.Close()

	exePath, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	exePath, _ = filepath.Abs(exePath)

	currentSettings, err := settingsStore.Get()
	if err != nil {
		fatal(err)
	}

	profileStore := profiles.NewStore(resolved.ProfilesFile)
	sw := &switcher.Switcher{
		ConfigPath: resolved.GrokConfig,
		Profiles:   profileStore,
	}
	if err := sw.EnsureDefaultProfile(); err != nil {
		crash.Logf("default profile import skipped: %v", err)
	}
	routingStore := routing.NewStore(resolved.RoutingFile)
	routingSnapshot, err := routingStore.Initialize(profileStore)
	if err != nil {
		fatal(err)
	}
	profileList, err := profileStore.List()
	if err != nil {
		fatal(err)
	}
	startupPolicy := routing.RepairPolicy(profileList, routingSnapshot.Policy)
	hydratedRouting, err := routing.ProjectWithPolicy(profileList, startupPolicy)
	if err != nil {
		fatal(err)
	}
	if err := sw.ApplyRouting(hydratedRouting); err != nil {
		fatal(fmt.Errorf("应用启动路由配置失败: %w", err))
	}
	if startupPolicy != routingSnapshot.Policy {
		if _, err := routingStore.UpdatePolicy(startupPolicy); err != nil {
			fatal(fmt.Errorf("修复启动路由策略失败: %w", err))
		}
	}

	if err := autostart.Sync(currentSettings.Autostart, exePath, currentSettings.SilentAutostart); err != nil {
		crash.Logf("autostart sync failed: %v", err)
	}
	home, _ := os.UserHomeDir()
	proxyManager := cliproxy.NewManager(resolved.DataDir, home, cliproxy.ResolveBuiltinBinary(exePath), cliproxy.DarwinKeychain{})
	sshHandler := ssh.NewHandler(resolved.DataDir)

	appServer := &server.Server{
		Paths:             resolved,
		Profiles:          profileStore,
		Routing:           routingStore,
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
		fatal(err)
	}
	if err := appServer.EnsureSubscriptionProxyRoutes(); err != nil {
		_ = httpServer.Shutdown(context.Background())
		fatal(fmt.Errorf("更新订阅代理路由失败: %w", err))
	}
	if err := appServer.ApplyCurrentRouting(); err != nil {
		_ = httpServer.Shutdown(context.Background())
		fatal(fmt.Errorf("最终应用组合路由失败: %w", err))
	}
	// Route net/http's internal panic/error reports into the crash log too.
	if crashFile := resolved.LogFile; crashFile != "" {
		if f, ferr := os.OpenFile(crashFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr == nil {
			httpServer.ErrorLog = log.New(f, "http: ", log.LstdFlags)
		}
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)

	trayApp := &tray.Tray{
		Profiles: profileStore,
		Settings: settingsStore,
		Switcher: sw,
		URL:      url,
		ExePath:  exePath,
		DataDir:  resolved.DataDir,
		LogFile:  resolved.LogFile,
		AuthFile: filepath.Join(resolved.GrokHome, "auth.json"),
		Assets:   assets,
	}
	if !*noTray {
		appServer.SetOnChanged(trayApp.Refresh)
	}

	if !*silent && currentSettings.AutoOpenBrowser {
		_ = tray.OpenBrowser(url)
	}

	if *noTray {
		waitForSignal()
		shutdown(httpServer)
		return
	}
	trayApp.Run()
	shutdown(httpServer)
}

func waitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

func shutdown(srv interface{ Shutdown(context.Context) error }) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func fatal(err error) {
	crash.ReportFatal(err)
	os.Exit(1)
}

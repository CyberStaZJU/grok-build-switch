//go:build wailsgui && darwin

package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"grok_switch/internal/crash"
	browsertray "grok_switch/internal/tray"
)

// guiTrayController implements menu-bar resident behavior on macOS.
// Closing the window hides it to the menu bar instead of exiting.
// The menu bar shows routing policy, cache stats, and quick actions.
type guiTrayController struct {
	url  string
	icon []byte

	ctxMu sync.RWMutex
	ctx   context.Context

	ready         atomic.Bool
	quitRequested atomic.Bool
	refreshCh     chan struct{}
	done          chan struct{}
	doneOnce      sync.Once

	providerClient *darwinTrayProviderClient

	// Menu item handles for dynamic updates.
	menuMu      sync.Mutex
	menuStop    *guiTrayMenuStopper
	lastMenu    string
	lastStats   cacheStatsSnapshot
	lastRouting routingSnapshot

	showAction func(context.Context)
	hideAction func(context.Context)
	quitAction func(context.Context)
}

type guiTrayMenuStopper struct {
	ch   chan struct{}
	once sync.Once
}

func newGUITrayMenuStopper() *guiTrayMenuStopper {
	return &guiTrayMenuStopper{ch: make(chan struct{})}
}

func (s *guiTrayMenuStopper) close() {
	if s != nil {
		s.once.Do(func() { close(s.ch) })
	}
}

func newGUITrayController(url string, icon []byte) *guiTrayController {
	return &guiTrayController{
		url:            url,
		icon:           icon,
		providerClient: newDarwinTrayProviderClient(url),
		refreshCh:      make(chan struct{}, 1),
		done:           make(chan struct{}),
		showAction: func(ctx context.Context) {
			wailsruntime.WindowShow(ctx)
			wailsruntime.WindowUnminimise(ctx)
		},
		hideAction: wailsruntime.WindowHide,
		quitAction: wailsruntime.Quit,
	}
}

func (t *guiTrayController) register() {
	// Use RunWithExternalLoop: Wails owns the Cocoa main loop, systray only registers.
	systray.Register(t.onReady, t.onExit)
}

func (t *guiTrayController) startup(ctx context.Context) {
	t.ctxMu.Lock()
	t.ctx = ctx
	t.ctxMu.Unlock()
	if t.quitRequested.Load() {
		t.quitAction(ctx)
	}
}

func (t *guiTrayController) context() context.Context {
	t.ctxMu.RLock()
	defer t.ctxMu.RUnlock()
	return t.ctx
}

// beforeClose intercepts the window close button. Returns true to prevent
// actual close — the window is hidden to the menu bar instead.
func (t *guiTrayController) beforeClose(ctx context.Context) bool {
	if t.quitRequested.Load() || !t.ready.Load() {
		return false
	}
	t.hideAction(ctx)
	return true
}

func (t *guiTrayController) showWindow() {
	if ctx := t.context(); ctx != nil {
		t.showAction(ctx)
	}
}

func (t *guiTrayController) requestQuit() {
	if !t.quitRequested.CompareAndSwap(false, true) {
		return
	}
	systray.Quit()
	if ctx := t.context(); ctx != nil {
		t.quitAction(ctx)
	}
}

func (t *guiTrayController) shutdown() {
	t.stop()
}

func (t *guiTrayController) onReady() {
	t.ready.Store(true)
	if len(t.icon) > 0 {
		systray.SetIcon(t.icon)
	}
	systray.SetTitle("")
	systray.SetTooltip("Grok Build Switch · 路由策略控制")
	t.refreshMenu(true)
	go t.refreshLoop()
}

func (t *guiTrayController) onExit() {
	t.ready.Store(false)
	t.stop()
}

func (t *guiTrayController) stop() {
	t.doneOnce.Do(func() { close(t.done) })
	t.menuMu.Lock()
	t.menuStop.close()
	t.menuStop = nil
	t.menuMu.Unlock()
}

func (t *guiTrayController) refreshLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			t.refreshMenu(false)
		case <-t.refreshCh:
			t.refreshMenu(true)
		}
	}
}

func (t *guiTrayController) requestRefresh() {
	select {
	case <-t.done:
		return
	case t.refreshCh <- struct{}{}:
	default:
	}
}

func (t *guiTrayController) refreshMenu(force bool) {
	snapshot, err := t.providerClient.snapshot(context.Background())
	if err != nil {
		// On error, still try to render with last known state.
		snapshot = t.lastRouting
	}
	stats, _ := t.providerClient.cacheStats(context.Background())

	key := snapshot.fingerprint() + "|" + stats.fingerprint()
	if !force && key == t.lastMenu {
		return
	}
	t.lastMenu = key
	t.lastRouting = snapshot
	t.lastStats = stats
	t.rebuildMenu(snapshot, stats, err)
}

func (t *guiTrayController) rebuildMenu(snapshot routingSnapshot, stats cacheStatsSnapshot, loadErr error) {
	t.menuMu.Lock()
	previous := t.menuStop
	stopper := newGUITrayMenuStopper()
	t.menuStop = stopper
	t.menuMu.Unlock()
	previous.close()

	systray.ResetMenu()

	// Header
	header := systray.AddMenuItem("Grok Build Switch", "路由策略与模型切换")
	header.Disable()
	systray.AddSeparator()

	// Current routing policy section
	policyMenu := systray.AddMenuItem("路由策略", "查看与切换路由策略")
	{
		defaultTitle := "Default: （未设置）"
		if snapshot.DefaultModel != "" {
			defaultTitle = "Default: " + snapshot.DefaultModel
		}
		defaultItem := policyMenu.AddSubMenuItem(defaultTitle, "当前默认模型")
		defaultItem.Disable()

		wsTitle := "Web Search: （未设置）"
		if snapshot.WebSearchModel != "" {
			wsTitle = "Web Search: " + snapshot.WebSearchModel
		}
		wsItem := policyMenu.AddSubMenuItem(wsTitle, "当前搜索模型")
		wsItem.Disable()

		expTitle := "Explore: （未设置）"
		if snapshot.ExploreModel != "" {
			expTitle = "Explore: " + snapshot.ExploreModel
		}
		expItem := policyMenu.AddSubMenuItem(expTitle, "当前 Explore 子代理模型")
		expItem.Disable()

		planTitle := "Plan: （未设置）"
		if snapshot.PlanModel != "" {
			planTitle = "Plan: " + snapshot.PlanModel
		}
		planItem := policyMenu.AddSubMenuItem(planTitle, "当前 Plan 子代理模型")
		planItem.Disable()
	}
	systray.AddSeparator()

	// Quick model switch section
	if len(snapshot.AvailableModels) > 0 {
		modelsMenu := systray.AddMenuItem("切换 Default 模型", "快速切换默认模型")
		for _, model := range snapshot.AvailableModels {
			item := modelsMenu.AddSubMenuItem(model.Name, model.ID)
			if model.Name == snapshot.DefaultModel {
				item.Check()
			}
			m := model
			t.watch(stopper.ch, item, "switch-default:"+m.ID, func() {
				if err := t.providerClient.updatePolicy(context.Background(), map[string]any{
					"default": m.Name,
				}); err != nil {
					crash.Logf("menu-bar switch default to %s failed: %v", m.Name, err)
					return
				}
				t.requestRefresh()
			})
		}
		systray.AddSeparator()
	}

	// Cache stats section
	statsMenu := systray.AddMenuItem("缓存与 Token", "查看缓存命中率与 Token 消耗")
	{
		hitRate := "N/A"
		if stats.HitRate != nil {
			hitRate = fmt.Sprintf("%.1f%%", *stats.HitRate*100)
		}
		hitItem := statsMenu.AddSubMenuItem("缓存命中率: "+hitRate, "最近 24h")
		hitItem.Disable()

		promptItem := statsMenu.AddSubMenuItem(fmt.Sprintf("Prompt Tokens: %s", formatTokens(stats.PromptTokens)), "总输入 token")
		promptItem.Disable()

		cachedItem := statsMenu.AddSubMenuItem(fmt.Sprintf("Cached Tokens: %s", formatTokens(stats.CachedPromptTokens)), "命中缓存的 token")
		cachedItem.Disable()

		completionItem := statsMenu.AddSubMenuItem(fmt.Sprintf("Completion Tokens: %s", formatTokens(stats.CompletionTokens)), "总输出 token")
		completionItem.Disable()

		turnsItem := statsMenu.AddSubMenuItem(fmt.Sprintf("推理轮次: %d", stats.Turns), "最近 24h 推理次数")
		turnsItem.Disable()
	}
	systray.AddSeparator()

	// Actions
	openWindow := systray.AddMenuItem("打开主窗口", "显示 Grok Build Switch 主界面")
	openBrowser := systray.AddMenuItem("打开 Web 管理界面", t.url)
	systray.AddSeparator()
	exit := systray.AddMenuItem("退出 Grok Build Switch", "完全退出应用")

	t.watch(stopper.ch, openWindow, "show", t.showWindow)
	t.watch(stopper.ch, openBrowser, "open-browser", func() {
		if err := browsertray.OpenBrowser(t.url); err != nil {
			crash.Logf("open GUI web interface: %v", err)
		}
	})
	t.watch(stopper.ch, exit, "quit", t.requestQuit)
}

func (t *guiTrayController) watch(stop <-chan struct{}, item *systray.MenuItem, name string, action func()) {
	go func() {
		for {
			select {
			case <-stop:
				return
			case _, ok := <-item.ClickedCh:
				if !ok {
					return
				}
				crash.Guard("gui-tray:"+name, action)
			}
		}
	}()
}

func formatTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

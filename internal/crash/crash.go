// Package crash captures diagnostics the GUI build would otherwise lose.
//
// grok_switch ships with -H windowsgui, so there is no console attached and
// the process disappears silently on a panic. crash.Setup opens the log file
// at paths.LogFile, redirects os.Stderr and the standard log package there,
// and exposes Guard() to recover panics from goroutines with a stack trace.
package crash

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

var (
	mu             sync.Mutex
	logFile        *os.File
	configuredPath string
	originalStderr = os.Stderr
)

// Setup opens logPath for appending and routes stderr + log through it.
// Safe to call with an empty path (no-op). It must be called as early as
// possible in main, before any goroutine might panic.
func Setup(logPath string) {
	mu.Lock()
	configuredPath = logPath
	mu.Unlock()
	if logPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	mu.Lock()
	logFile = f
	mu.Unlock()
	// os.Stderr is an *os.File, so point it straight at the log. Anything
	// written via fmt.Fprintln(os.Stderr, ...) now lands in the file.
	os.Stderr = f
	log.SetOutput(f)
	Logf("=== grok_switch started %s ===", time.Now().Format("2006-01-02 15:04:05"))
}

// ReportFatal records and displays a fatal startup error. Windows GUI builds
// have no console, so the platform implementation uses a native dialog there.
func ReportFatal(err error) {
	if err == nil {
		return
	}
	_, _ = fmt.Fprintln(originalStderr, err)
	Logf("FATAL: %v", err)

	mu.Lock()
	f := logFile
	path := configuredPath
	mu.Unlock()
	if f != nil {
		_ = f.Sync()
	}

	message := err.Error()
	if f != nil && path != "" {
		message += "\n\n诊断日志：" + path
	} else if path != "" {
		message += "\n\n日志目录不可写，原定日志路径：" + path
	}
	showErrorDialog("grok_switch 启动失败", message)
}

// ShowInfo displays a short informational message when the platform supports
// it. It is used for duplicate-instance feedback before the second process exits.
func ShowInfo(title, message string) {
	showInfoDialog(title, message)
}

// Logf appends a line to the crash log. It never panics.
func Logf(format string, args ...any) {
	mu.Lock()
	f := logFile
	mu.Unlock()
	if f == nil {
		return
	}
	_, _ = fmt.Fprintf(f, format+"\n", args...)
}

// Guard runs fn and, on panic, records the value and stack trace to the log
// instead of letting it crash the process silently. Use it to wrap goroutines
// that have no recovering caller (tray clicks, background workers).
func Guard(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			Logf("panic in %s: %v\n%s", name, r, debug.Stack())
		}
	}()
	fn()
}

// RecoverMainThread is meant for `defer RecoverMainThread()` at the top of
// main. It writes the panic + stack, flushes, then re-panics so the exit code
// stays non-zero.
func RecoverMainThread() {
	if r := recover(); r != nil {
		Logf("PANIC (main): %v\n%s", r, debug.Stack())
		ReportFatal(fmt.Errorf("程序发生未处理异常：%v", r))
		mu.Lock()
		if logFile != nil {
			_ = logFile.Sync()
			_ = logFile.Close()
		}
		mu.Unlock()
		panic(r)
	}
}

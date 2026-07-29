package codebuddy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

var fallbackModels = []string{
	"hy3",
	"glm-5.2",
	"glm-5.1",
	"glm-5.0",
	"glm-5.0-turbo",
	"glm-5v-turbo",
	"glm-4.7",
	"minimax-m3-pay",
	"minimax-m2.7",
	"kimi-k3-2",
	"kimi-k2.7",
	"kimi-k2.6",
	"deepseek-v4-pro",
	"deepseek-v4-flash",
	"deepseek-v3-2-volc",
}

// Status is a side-effect-free snapshot of the locally installed CLI.
type Status struct {
	Available bool     `json:"available"`
	Path      string   `json:"path,omitempty"`
	Version   string   `json:"version,omitempty"`
	Models    []string `json:"models,omitempty"`
	Fallback  bool     `json:"fallback,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// ResolveExecutable locates CodeBuddy without inspecting configuration or
// authentication files. An override always takes precedence.
func ResolveExecutable(override string) (string, error) {
	var candidates []string
	if override = strings.TrimSpace(override); override != "" {
		candidates = append(candidates, override)
	} else {
		for _, name := range []string{"codebuddy", "cbc"} {
			if path, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, path)
			}
		}
		if runtime.GOOS == "darwin" {
			candidates = append(candidates, "/opt/homebrew/bin/codebuddy", "/usr/local/bin/codebuddy")
		}
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates,
				filepath.Join(home, ".local", "bin", executableName("codebuddy")),
				filepath.Join(home, "bin", executableName("codebuddy")),
			)
		}
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil || seen[absolute] {
			continue
		}
		seen[absolute] = true
		info, err := os.Stat(absolute)
		if err == nil && !info.IsDir() {
			return absolute, nil
		}
	}
	if override != "" {
		return "", fmt.Errorf("找不到指定的 CodeBuddy 可执行文件: %s", override)
	}
	return "", errors.New("未找到 codebuddy，请先安装 CodeBuddy Code")
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Inspect obtains version and model information exclusively from public CLI
// flags. It never opens CodeBuddy settings, session, or authentication files.
func Inspect(ctx context.Context, override string) Status {
	path, err := ResolveExecutable(override)
	if err != nil {
		return Status{Error: err.Error()}
	}
	status := Status{Available: true, Path: path}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if output, runErr := exec.CommandContext(probeCtx, path, "--version").CombinedOutput(); runErr == nil {
		status.Version = firstNonEmptyLine(string(output))
	} else {
		status.Error = commandError("读取 CodeBuddy 版本失败", output, runErr).Error()
	}
	output, helpErr := exec.CommandContext(probeCtx, path, "--help").CombinedOutput()
	if helpErr == nil {
		status.Models = ParseModelsFromHelp(string(output))
	}
	if len(status.Models) == 0 {
		status.Models = FallbackModels()
		status.Fallback = true
		if helpErr != nil && status.Error == "" {
			status.Error = commandError("读取 CodeBuddy 帮助失败", output, helpErr).Error()
		}
	}
	return status
}

// ParseModelsFromHelp extracts the parenthesized list printed on the --model
// option line. Unknown help layouts safely return no models and trigger the
// caller's static fallback.
func ParseModelsFromHelp(help string) []string {
	lines := strings.Split(strings.ReplaceAll(help, "\r\n", "\n"), "\n")
	var modelText string
	collecting := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "--model") && strings.Contains(trimmed, "<model>") {
			modelText = trimmed
			collecting = true
			continue
		}
		if collecting {
			if strings.HasPrefix(trimmed, "-") || strings.HasSuffix(modelText, ")") {
				break
			}
			modelText += " " + trimmed
		}
	}
	start := strings.LastIndex(modelText, "(")
	end := strings.Index(modelText[start+1:], ")")
	if start < 0 || end < 0 {
		return nil
	}
	inside := modelText[start+1 : start+1+end]
	seen := map[string]bool{}
	models := make([]string, 0, 16)
	for _, field := range strings.Split(inside, ",") {
		model := strings.Trim(strings.TrimSpace(field), "`'\"")
		if model == "" || strings.ContainsAny(model, " <>\t") || seen[model] {
			continue
		}
		seen[model] = true
		models = append(models, model)
	}
	return models
}

func FallbackModels() []string {
	models := append([]string(nil), fallbackModels...)
	return models
}

func SortedModels(models []string) []string {
	result := append([]string(nil), models...)
	sort.Strings(result)
	return result
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func commandError(prefix string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %s: %w", prefix, message, err)
}

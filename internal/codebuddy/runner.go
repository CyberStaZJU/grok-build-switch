package codebuddy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

const emptyMCPConfig = `{"mcpServers":{}}`

// RunRequest intentionally exposes no arbitrary CLI arguments, environment
// overrides, persistence, permission bypass, background, or daemon controls.
type RunRequest struct {
	Prompt string
	Cwd    string
	Model  string
}

// Runner starts one foreground, non-persistent, read-only headless invocation.
type Runner struct {
	Executable string
}

// DefaultArgs returns the security baseline used for every run.
// CodeBuddy executes tools natively with acceptEdits permission mode;
// the proxy layer transparently forwards tool events to the client.
func DefaultArgs(model string) []string {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--permission-mode", "acceptEdits",
		"--tools", "default",
		"--no-session-persistence",
		"--setting-sources", "user",
		"--strict-mcp-config",
		"--mcp-config", emptyMCPConfig,
	}
	if model = strings.TrimSpace(model); model != "" {
		args = append(args, "--model", model)
	}
	return args
}

// Run streams normalized events to emit. The prompt is written through stdin
// instead of process arguments so it is not exposed in process listings.
func (r Runner) Run(ctx context.Context, request RunRequest, emit func(Event) error) error {
	if emit == nil {
		return errors.New("CodeBuddy 事件处理函数不能为空")
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return errors.New("CodeBuddy prompt 不能为空")
	}
	if err := validateModel(request.Model); err != nil {
		return err
	}
	executable, err := ResolveExecutable(r.Executable)
	if err != nil {
		return err
	}
	cwd, err := normalizeCwd(request.Cwd)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, DefaultArgs(request.Model)...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS=1")
	cmd.Stdin = strings.NewReader(prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 CodeBuddy stdout 失败: %w", err)
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 CodeBuddy 失败: %w", err)
	}

	decoder := NewEventDecoder(stdout)
	var consumeErr error
	for {
		event, nextErr := decoder.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			consumeErr = nextErr
			break
		}
		if err := emit(event); err != nil {
			consumeErr = err
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			break
		}
	}
	waitErr := cmd.Wait()
	if consumeErr != nil {
		return consumeErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return commandError("CodeBuddy 执行失败", stderr.Bytes(), waitErr)
	}
	return nil
}

func validateModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	if len(model) > 128 {
		return errors.New("CodeBuddy model 过长")
	}
	for _, character := range model {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return fmt.Errorf("无效的 CodeBuddy model: %q", model)
		}
	}
	if strings.HasPrefix(model, "-") {
		return fmt.Errorf("无效的 CodeBuddy model: %q", model)
	}
	return nil
}

func normalizeCwd(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("读取当前工作目录失败: %w", err)
		}
	}
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("解析 CodeBuddy 工作目录失败: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("访问 CodeBuddy 工作目录失败: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("CodeBuddy 工作目录不是目录: %s", absolute)
	}
	return absolute, nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const limit = 64 << 10
	original := len(p)
	if b.buffer.Len() < limit {
		remaining := limit - b.buffer.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return original, nil
}

func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }

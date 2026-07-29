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

// Capability controls the native CodeBuddy tools exposed to one run.
type Capability string

const (
	CapabilityReadOnly Capability = "read-only"
	CapabilityWeb      Capability = "web"
	CapabilityExecute  Capability = "execute"
	CapabilityAll      Capability = "all"
)

const readOnlyRunGuidance = `CodeBuddy 本地桥接器运行约束（由桥接器提供，不是对话记录）：
- 当前只提供 Read、Grep、Glob 三种只读工具。
- Bash、Write、Edit、后台任务和 MCP 均不可用；不要尝试调用或声称将调用这些能力。
- 需要检查大型工作区时，先读取已知文件并检查明确的子目录，再使用精确的 Grep 或 Glob 模式。
- 不要从工作区根目录并行发起多个宽泛的递归 Glob。若搜索超时，应缩小到相关目录（例如 src、scripts、docs 或测试目录）并收紧模式，而不是原样重试。
- 如果任务确实需要执行命令或修改文件，请明确说明当前只读桥接器无法完成该步骤，并给出基于现有只读证据的结果。`

const isolatedExecuteGuidance = `CodeBuddy 本地桥接器运行约束（由桥接器提供，不是对话记录）：
- 当前提供 Read、Grep、Glob、Bash；Write 和 Edit 不可用。
- 你在独立的 detached Git worktree 中运行，不要访问或修改该 worktree 之外的路径。
- Bash 用于检查、构建和测试。不要执行网络请求、提权、后台任务、守护进程、删除 worktree 或破坏 Git 历史。
- 大型工作区应先检查明确目录并使用精确搜索；不要并行发起多个宽泛的根目录递归 Glob。
- 完成后说明执行过的命令和结果。`

const webRunGuidance = `CodeBuddy 本地桥接器运行约束（由桥接器提供，不是对话记录）：
- 当前提供 Read、Grep、Glob、WebSearch、WebFetch。
- 联网问题应优先使用 WebSearch 获取候选结果，再用 WebFetch 读取最相关页面；一次失败后应换查询或结果，不要机械重复同一请求。
- Bash、Write、Edit、后台任务和 MCP 均不可用。
- 搜索结果必须区分成功、无结果和网络/站点失败，不要把“已尝试”描述成“已获取内容”。`

const isolatedAllGuidance = `CodeBuddy 本地桥接器运行约束（由桥接器提供，不是对话记录）：
- 当前提供 Read、Grep、Glob、Bash、Write、Edit，可检查、修改、构建和测试代码。
- 你在独立的 detached Git worktree 中运行。只允许修改该 worktree 内的项目文件，不要访问或修改外部路径。
- 不要执行网络请求、提权、后台任务、守护进程、删除 worktree、提交、推送或破坏 Git 历史。
- 修改前先检查相关文件；大型工作区使用精确搜索，避免多个宽泛的根目录递归 Glob。
- 完成后总结修改文件、执行过的测试，并明确说明改动仍位于隔离 worktree，尚未应用到用户当前工作区。`

func buildRunPrompt(prompt string, capability Capability, worktree string) string {
	guidance := readOnlyRunGuidance
	switch capability {
	case CapabilityWeb:
		guidance = webRunGuidance
	case CapabilityExecute:
		guidance = isolatedExecuteGuidance
	case CapabilityAll:
		guidance = isolatedAllGuidance
	}
	if worktree != "" {
		guidance += "\n- 本次隔离 worktree：" + worktree
	}
	return guidance + "\n\n--- 对话记录开始 ---\n" + strings.TrimSpace(prompt)
}

// RunRequest intentionally exposes no arbitrary CLI arguments, environment
// overrides, persistence, permission bypass, background, or daemon controls.
type RunRequest struct {
	Prompt     string
	Cwd        string
	Model      string
	Capability Capability
}

// Runner starts one foreground, non-persistent CodeBuddy invocation.
type Runner struct {
	Executable string
}

// DefaultArgs returns the security baseline used for one capability mode.
// Non-read-only modes still disable MCP, persistence, permission bypasses, and
// background tasks. Their filesystem changes run in a detached Git worktree.
func DefaultArgs(model string, capability Capability) []string {
	tools := "Read,Grep,Glob"
	permissionMode := "default"
	switch capability {
	case CapabilityWeb:
		tools = "Read,Grep,Glob,WebSearch,WebFetch"
	case CapabilityExecute:
		tools = "Read,Grep,Glob,Bash"
		permissionMode = "acceptEdits"
	case CapabilityAll:
		tools = "Read,Grep,Glob,Bash,Write,Edit"
		permissionMode = "acceptEdits"
	}
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--permission-mode", permissionMode,
		"--tools", tools,
	}
	if capability == CapabilityWeb {
		args = append(args, "--allowedTools", "WebSearch,WebFetch")
	} else if capability == CapabilityExecute {
		args = append(args, "--allowedTools", "Bash")
	} else if capability == CapabilityAll {
		args = append(args, "--allowedTools", "Bash,Write,Edit")
	}
	args = append(args,
		"--no-session-persistence",
		"--setting-sources", "user",
		"--strict-mcp-config",
		"--mcp-config", emptyMCPConfig,
	)
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
	capability, err := normalizeCapability(request.Capability)
	if err != nil {
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
	worktree := ""
	if capability != CapabilityReadOnly && capability != CapabilityWeb {
		worktree, err = createIsolatedWorktree(ctx, cwd)
		if err != nil {
			return err
		}
		cwd = worktree
		if err := emit(Event{Kind: EventMeta, Type: "grok_switch_worktree", Text: worktree}); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, executable, DefaultArgs(request.Model, capability)...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS=1")
	cmd.Stdin = strings.NewReader(buildRunPrompt(prompt, capability, worktree))
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

func normalizeCapability(capability Capability) (Capability, error) {
	switch capability {
	case "", CapabilityReadOnly:
		return CapabilityReadOnly, nil
	case CapabilityWeb, CapabilityExecute, CapabilityAll:
		return capability, nil
	default:
		return "", fmt.Errorf("无效的 CodeBuddy capability: %q", capability)
	}
}

func createIsolatedWorktree(ctx context.Context, cwd string) (string, error) {
	root, err := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("CodeBuddy 非只读模式要求工作目录位于 Git 仓库中")
	}
	status, err := gitOutput(ctx, root, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", errors.New("CodeBuddy 隔离执行要求干净 Git 工作区；请先提交、暂存到其他安全位置或改用只读模式")
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("读取用户缓存目录失败: %w", err)
	}
	repoName := filepath.Base(root)
	base := filepath.Join(cacheDir, "Grok Build Switch", "codebuddy-worktrees", repoName)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("创建 CodeBuddy worktree 目录失败: %w", err)
	}
	worktree, err := os.MkdirTemp(base, "run-*")
	if err != nil {
		return "", fmt.Errorf("创建 CodeBuddy worktree 路径失败: %w", err)
	}
	if err := os.Remove(worktree); err != nil {
		return "", fmt.Errorf("准备 CodeBuddy worktree 路径失败: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", worktree, "HEAD")
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(worktree)
		return "", commandError("创建 CodeBuddy 隔离 worktree 失败", stderr.Bytes(), err)
	}
	return worktree, nil
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", commandError("Git 命令失败", stderr.Bytes(), err)
	}
	return strings.TrimSpace(string(output)), nil
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

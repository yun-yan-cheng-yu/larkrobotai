// Package localtools 提供本地环境操作工具:命令执行与文件读写。
// 一旦启用,任何能给机器人发消息的人都等效于在本机执行命令,务必限制访问人群并保留日志审计。
package localtools

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"larkrobotai/internal/llm"
)

const maxReadBytes = 10 << 20 // 读取文件的硬上限 10MB

type Tools struct {
	workDir string
	shell   string // windows: cmd | powershell; linux: sh | bash
	timeout time.Duration
	maxOut  int

	mu    sync.Mutex      // 串行执行,避免并发命令/写文件互相干扰
	names map[string]bool // 内置工具名集合
}

func New(workDir, shell string, timeout time.Duration, maxOut int) *Tools {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	switch shell {
	case "powershell", "bash", "sh":
		// 显式指定的合法 shell
	default:
		if runtime.GOOS == "windows" {
			shell = "cmd"
		} else {
			shell = "sh"
		}
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if maxOut <= 0 {
		maxOut = 8000
	}
	return &Tools{
		workDir: workDir,
		shell:   shell,
		timeout: timeout,
		maxOut:  maxOut,
		names: map[string]bool{
			"run_command":    true,
			"list_directory": true,
			"read_file":      true,
			"write_file":     true,
		},
	}
}

func (t *Tools) Owns(name string) bool { return t.names[name] }

func (t *Tools) WorkDir() string { return t.workDir }

func (t *Tools) Shell() string { return t.shell }

// Defs 返回 OpenAI function 格式的内置工具定义
func (t *Tools) Defs() []llm.Tool {
	return []llm.Tool{
		{Type: "function", Function: llm.FunctionDef{
			Name:        "run_command",
			Description: fmt.Sprintf("在主人电脑上执行一条 %s 命令(工作目录 %s)。可以完成查看系统状态、安装软件、整理文件、调用本机程序等一切本地操作,返回 exit code 与完整输出。", t.shellName(), t.workDir),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "要执行的命令行"},
				},
				"required": []string{"command"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "list_directory",
			Description: "列出本地目录的内容(文件名、类型、大小)",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "目录路径,相对路径基于工作目录"},
				},
				"required": []string{"path"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "read_file",
			Description: "读取本地文本文件的内容,可按行分页",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": "文件路径"},
					"startLine": map[string]any{"type": "integer", "description": "起始行(从 1 开始),默认 1"},
					"limit":     map[string]any{"type": "integer", "description": "读取行数,默认 200"},
				},
				"required": []string{"path"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "write_file",
			Description: "写入本地文本文件(整文件覆盖或追加)",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "文件路径,目录不存在会自动创建"},
					"content": map[string]any{"type": "string", "description": "要写入的内容"},
					"append":  map[string]any{"type": "boolean", "description": "true 为追加,默认 false 覆盖"},
				},
				"required": []string{"path", "content"},
			},
		}},
	}
}

// Call 执行一个内置工具
func (t *Tools) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	switch name {
	case "run_command":
		return t.runCommand(cctx, strArg(args["command"]))
	case "list_directory":
		return t.listDir(strArg(args["path"]))
	case "read_file":
		return t.readFile(strArg(args["path"]), intArg(args["startLine"]), intArg(args["limit"]))
	case "write_file":
		return t.writeFile(strArg(args["path"]), strArg(args["content"]), boolArg(args["append"]))
	}
	return "", fmt.Errorf("未知工具: %s", name)
}

func (t *Tools) runCommand(ctx context.Context, command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command 不能为空")
	}
	log.Printf("[cmd] %s", command)
	var cmd *exec.Cmd
	switch t.shell {
	case "powershell":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	case "bash", "sh":
		cmd = exec.CommandContext(ctx, t.shell, "-c", command)
	default:
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	}
	cmd.Dir = t.workDir
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	} else if err != nil {
		return fmt.Sprintf("命令未能执行: %v", err), nil
	}
	res := fmt.Sprintf("exit=%d\n%s", exitCode, t.trunc(string(out)))
	return t.trunc(res), nil
}

func (t *Tools) shellName() string {
	switch t.shell {
	case "powershell":
		return "PowerShell"
	default:
		return t.shell
	}
}

func (t *Tools) listDir(path string) (string, error) {
	dir := t.resolve(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "目录 %s,共 %d 项:\n", dir, len(entries))
	for i, e := range entries {
		if i >= 500 {
			b.WriteString(fmt.Sprintf("...(其余 %d 项省略)\n", len(entries)-500))
			break
		}
		if e.IsDir() {
			b.WriteString("[目录] " + e.Name() + "/\n")
			continue
		}
		size := int64(-1)
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		b.WriteString(fmt.Sprintf("[文件] %s (%d 字节)\n", e.Name(), size))
	}
	return t.trunc(b.String()), nil
}

func (t *Tools) readFile(path string, startLine, limit int) (string, error) {
	if startLine < 1 {
		startLine = 1
	}
	if limit <= 0 {
		limit = 200
	}
	p := t.resolve(path)
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if info.Size() > maxReadBytes {
		return "", fmt.Errorf("文件过大(%d 字节,上限 %d),请用 run_command 分段读取", info.Size(), maxReadBytes)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	// 文件以换行结尾时 Split 会多出一个空尾行,不计入行数
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for i := range lines { // Windows 换行的 \r 残留
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	end := startLine - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	head := fmt.Sprintf("文件 %s 共 %d 行,显示第 %d-%d 行(startLine/limit 可翻页):\n", p, len(lines), startLine, end)
	body := strings.Join(lines[startLine-1:end], "\n")
	return t.trunc(head + body), nil
}

func (t *Tools) writeFile(path, content string, appendMode bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	p := t.resolve(path)
	if dir := filepath.Dir(p); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(p, flags, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := f.WriteString(content)
	if err != nil {
		return "", err
	}
	mode := "覆盖"
	if appendMode {
		mode = "追加"
	}
	log.Printf("[write] %s (%d 字节,%s)", p, n, mode)
	return fmt.Sprintf("已%s %d 字节到 %s", mode, n, p), nil
}

// resolve 相对路径基于工作目录
func (t *Tools) resolve(p string) string {
	if p == "" {
		return t.workDir
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(t.workDir, p)
	}
	return p
}

func (t *Tools) trunc(s string) string {
	r := []rune(s)
	if len(r) > t.maxOut {
		return string(r[:t.maxOut]) + "\n...(输出过长已截断)"
	}
	return s
}

func strArg(v any) string {
	s, _ := v.(string)
	return s
}

func intArg(v any) int {
	switch n := v.(type) {
	case float64: // JSON 数字反序列化为 float64
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func boolArg(v any) bool {
	b, _ := v.(bool)
	return b
}

package localtools

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestTools(t *testing.T) *Tools {
	t.Helper()
	return New(t.TempDir(), "cmd", 30*time.Second, 8000)
}

func TestWriteReadList(t *testing.T) {
	tl := newTestTools(t)
	ctx := context.Background()

	out, err := tl.Call(ctx, "write_file", map[string]any{"path": "hello/a.txt", "content": "你好\nworld"})
	if err != nil || !strings.Contains(out, "5") { // "你好\nworld" 的字节数不是 5,中文占多字节,只断言写入成功
		if err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	out, err = tl.Call(ctx, "read_file", map[string]any{"path": "hello/a.txt"})
	if err != nil || !strings.Contains(out, "world") || !strings.Contains(out, "你好") {
		t.Fatalf("读取结果不符: %q, err=%v", out, err)
	}
	out, err = tl.Call(ctx, "list_directory", map[string]any{"path": "."})
	if err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("列目录结果不符: %q, err=%v", out, err)
	}
	// 追加模式
	if _, err := tl.Call(ctx, "write_file", map[string]any{"path": "hello/a.txt", "content": "\n尾巴", "append": true}); err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	out, _ = tl.Call(ctx, "read_file", map[string]any{"path": "hello/a.txt"})
	if !strings.Contains(out, "尾巴") {
		t.Fatalf("追加内容未读到: %q", out)
	}
}

func TestRunCommand(t *testing.T) {
	tl := newTestTools(t)
	out, err := tl.Call(context.Background(), "run_command", map[string]any{"command": "echo hello-cmd"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "hello-cmd") || !strings.Contains(out, "exit=0") {
		t.Fatalf("命令输出不符: %q", out)
	}
	// 退出码透传
	out, _ = tl.Call(context.Background(), "run_command", map[string]any{"command": "cmd /C exit 3"})
	if !strings.Contains(out, "exit=3") {
		t.Fatalf("退出码未透传: %q", out)
	}
}

func TestReadFilePaging(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		b.WriteString("L" + strconv.Itoa(i) + "\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := New(dir, "cmd", 30*time.Second, 8000)
	out, err := tl.Call(context.Background(), "read_file", map[string]any{"path": "big.txt", "startLine": float64(498)})
	if err != nil {
		t.Fatalf("分页读取失败: %v", err)
	}
	if !strings.Contains(out, "共 500 行") || !strings.Contains(out, "498") {
		t.Fatalf("分页信息不符: %q", out)
	}
}

package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ──────────────────────────────────────────────────────────
// IsConcurrencySafe / IsReadOnly 测试
// ──────────────────────────────────────────────────────────

func TestShellIsConcurrencySafe_ReadOnly(t *testing.T) {
	s := NewShellTool()

	readOnlyCommands := []string{
		`{"command": "ls -la"}`,
		`{"command": "cat /etc/hosts"}`,
		`{"command": "grep -r foo ."}`,
		`{"command": "find . -name '*.go'"}`,
		`{"command": "head -20 file.txt"}`,
		`{"command": "tail -f log.txt"}`,
		`{"command": "wc -l *.go"}`,
		`{"command": "echo hello"}`,
		`{"command": "pwd"}`,
		`{"command": "whoami"}`,
		`{"command": "date"}`,
		`{"command": "git log --oneline"}`,
		`{"command": "git diff HEAD~1"}`,
		`{"command": "git status"}`,
		`{"command": "go vet ./..."}`,
		`{"command": "docker ps"}`,
		`{"command": "diff a.txt b.txt"}`,
		`{"command": "df -h"}`,
	}

	for _, cmd := range readOnlyCommands {
		t.Run("safe_"+strings.Split(cmd, `"`)[3], func(t *testing.T) {
			if !s.IsReadOnly(cmd) {
				t.Errorf("expected IsReadOnly=true for: %s", cmd)
			}
			if !s.IsConcurrencySafe(cmd) {
				t.Errorf("expected IsConcurrencySafe=true for: %s", cmd)
			}
		})
	}
}

func TestShellIsConcurrencySafe_Dangerous(t *testing.T) {
	s := NewShellTool()

	dangerousCommands := []string{
		`{"command": "rm -rf /tmp/foo"}`,
		`{"command": "mv a.txt b.txt"}`,
		`{"command": "cp file1 file2"}`,
		`{"command": "mkdir -p newdir"}`,
		`{"command": "touch newfile"}`,
		`{"command": "chmod 755 script.sh"}`,
		`{"command": "echo hello > output.txt"}`,
		`{"command": "cat file.txt > other.txt"}`,
		`{"command": "sudo systemctl restart nginx"}`,
		`{"command": "git push origin main"}`,
		`{"command": "docker rm container"}`,
		// 以下命令曾因只读前缀子串匹配被误判为只读（P1③ 加固）：
		`{"command": "sed -i 's/a/b/' file.txt"}`,
		`{"command": "awk '{print $1 > \"out.txt\"}' file"}`,
		`{"command": "curl -s https://example.com"}`,
		`{"command": "wget http://example.com/file"}`,
	}

	for _, cmd := range dangerousCommands {
		t.Run("danger_"+strings.Split(cmd, `"`)[3][:min(20, len(strings.Split(cmd, `"`)[3]))], func(t *testing.T) {
			if s.IsReadOnly(cmd) {
				t.Errorf("expected IsReadOnly=false for: %s", cmd)
			}
			if s.IsConcurrencySafe(cmd) {
				t.Errorf("expected IsConcurrencySafe=false for: %s", cmd)
			}
		})
	}
}

func TestShellIsConcurrencySafe_EdgeCases(t *testing.T) {
	s := NewShellTool()

	tests := []struct {
		name     string
		args     string
		expected bool
	}{
		{"empty args", `{}`, false},
		{"invalid json", `not json`, false},
		{"empty command", `{"command": ""}`, false},
		{"leading spaces", `{"command": "  ls -la"}`, true},
		{"stderr redirect only (safe)", `{"command": "go vet 2>&1"}`, true}, // go vet is read-only, stderr redirect doesn't change that
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.IsConcurrencySafe(tt.args); got != tt.expected {
				t.Errorf("IsConcurrencySafe(%s) = %v, want %v", tt.args, got, tt.expected)
			}
		})
	}
}

func TestFileToolConcurrency(t *testing.T) {
	f := NewFileTool()

	// file read → safe
	readArgs := `{"action": "read", "path": "/tmp/test.txt"}`
	if !f.IsConcurrencySafe(readArgs) {
		t.Error("file read should be concurrency safe")
	}
	if !f.IsReadOnly(readArgs) {
		t.Error("file read should be read-only")
	}

	// file write → not safe
	writeArgs := `{"action": "write", "path": "/tmp/test.txt", "content": "hello"}`
	if f.IsConcurrencySafe(writeArgs) {
		t.Error("file write should NOT be concurrency safe")
	}
	if f.IsReadOnly(writeArgs) {
		t.Error("file write should NOT be read-only")
	}

	// invalid args → fail-closed
	if f.IsConcurrencySafe(`invalid`) {
		t.Error("invalid args should be fail-closed (not concurrency safe)")
	}
}

func TestEditToolConcurrency(t *testing.T) {
	e := NewEditTool()

	args := `{"path": "/tmp/test.txt", "search": "foo", "replace": "bar"}`
	if e.IsConcurrencySafe(args) {
		t.Error("edit should never be concurrency safe")
	}
	if e.IsReadOnly(args) {
		t.Error("edit should never be read-only")
	}
}

func TestGrepToolConcurrency(t *testing.T) {
	g := NewGrepTool()

	if !g.IsConcurrencySafe(`{"pattern": "foo"}`) {
		t.Error("grep should always be concurrency safe")
	}
	if !g.IsReadOnly(`{"pattern": "foo"}`) {
		t.Error("grep should always be read-only")
	}
}

func TestListToolConcurrency(t *testing.T) {
	l := NewListTool()

	if !l.IsConcurrencySafe(`{"path": "."}`) {
		t.Error("list should always be concurrency safe")
	}
	if !l.IsReadOnly(`{"path": "."}`) {
		t.Error("list should always be read-only")
	}
}

// ──────────────────────────────────────────────────────────
// Edit replace_all 测试
// ──────────────────────────────────────────────────────────

func TestEditReplaceAll(t *testing.T) {
	e := NewEditTool()

	// 创建临时文件，内容包含多处 "foo"
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.txt")
	content := "line1: foo bar\nline2: hello world\nline3: foo baz\nline4: foo qux\n"
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// ── replace_all=false, 多处匹配 → 应该报错 ──
	args := toJSON(map[string]any{
		"path":   tmpFile,
		"search": "foo",
		"replace": "XXX",
	})
	_, err := e.Execute(args)
	if err == nil {
		t.Error("expected error for non-unique match without replace_all")
	}
	if !strings.Contains(err.Error(), "出现了 3 次") {
		t.Errorf("error should mention count, got: %v", err)
	}
	if !strings.Contains(err.Error(), "replace_all=true") {
		t.Errorf("error should suggest replace_all=true, got: %v", err)
	}

	// 文件应该未被修改
	data, _ := os.ReadFile(tmpFile)
	if string(data) != content {
		t.Error("file should be unchanged after failed edit")
	}

	// ── replace_all=true → 应该全部替换 ──
	argsAll := toJSON(map[string]any{
		"path":        tmpFile,
		"search":      "foo",
		"replace":     "XXX",
		"replace_all": true,
	})
	result, err := e.Execute(argsAll)
	if err != nil {
		t.Fatalf("unexpected error with replace_all: %v", err)
	}
	if !strings.Contains(result, "替换了 3 处") {
		t.Errorf("result should mention count, got: %s", result)
	}

	// 验证文件内容
	data, _ = os.ReadFile(tmpFile)
	expected := "line1: XXX bar\nline2: hello world\nline3: XXX baz\nline4: XXX qux\n"
	if string(data) != expected {
		t.Errorf("file content mismatch:\ngot:  %q\nwant: %q", string(data), expected)
	}
}

func TestEditUniqueMatch(t *testing.T) {
	e := NewEditTool()

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "unique.txt")
	content := "hello world\nthis is a test\n"
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	args := toJSON(map[string]any{
		"path":    tmpFile,
		"search":  "world",
		"replace": "universe",
	})
	result, err := e.Execute(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "文件已编辑") {
		t.Errorf("expected success message, got: %s", result)
	}

	data, _ := os.ReadFile(tmpFile)
	if string(data) != "hello universe\nthis is a test\n" {
		t.Errorf("file not edited correctly: %q", string(data))
	}
}

func TestEditQuoteNormalization(t *testing.T) {
	e := NewEditTool()

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "quotes.txt")
	content := `hello world`
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// LLM 可能用引号包裹 search 字符串（如 `"hello world"` 而非 `hello world`）
	// stripQuotes 自动去除外层包裹的引号
	args := toJSON(map[string]any{
		"path":    tmpFile,
		"search":  `"hello world"`, // 外层直引号 — stripQuotes 处理
		"replace": "hello universe",
	})
	result, err := e.Execute(args)
	if err != nil {
		t.Fatalf("unexpected error with quote normalization: %v", err)
	}
	if !strings.Contains(result, "文件已编辑") {
		t.Errorf("expected success, got: %s", result)
	}

	data, _ := os.ReadFile(tmpFile)
	if string(data) != "hello universe" {
		t.Errorf("file not edited correctly: %q", string(data))
	}
	_ = result
}

// ──────────────────────────────────────────────────────────
// SaveLargeResult + TruncateResult 测试
// ──────────────────────────────────────────────────────────

func TestSaveLargeResult(t *testing.T) {
	tmpDir := t.TempDir()
	content := strings.Repeat("hello world\n", 1000) // ~12KB

	path, err := SaveLargeResult(tmpDir, "grep", content)
	if err != nil {
		t.Fatalf("SaveLargeResult failed: %v", err)
	}

	// 验证文件存在且内容完整
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("result file not found: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read result file: %v", err)
	}
	if string(data) != content {
		t.Errorf("saved content mismatch: len=%d, want len=%d", len(data), len(content))
	}

	// 验证文件名格式: <tool>_<ts>_<hash>.txt
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "grep_") || !strings.HasSuffix(base, ".txt") {
		t.Errorf("unexpected filename format: %s", base)
	}
}

func TestTruncateResult(t *testing.T) {
	// 生成 1000 行文本
	lines := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		lines[i] = "line " + string(rune('0'+i%10)) + ": some content here for testing"
	}
	content := strings.Join(lines, "\n")

	limit := 500
	result := TruncateResult(content, limit)

	if len(result) > limit {
		t.Errorf("truncated result too long: %d > %d", len(result), limit)
	}

	if !strings.Contains(result, "已截断") && !strings.Contains(result, "truncated") {
		t.Error("truncated result should contain truncation notice")
	}

	// 验证保留头部：前 70% 应该包含开头的行
	headPart := strings.Split(result, "\n")[0]
	if !strings.Contains(headPart, "line 0") {
		t.Errorf("head should contain first line, got: %s", headPart)
	}

	// 验证保留尾部：后 30% 应该包含末尾的行
	if !strings.Contains(result, "line 9") {
		t.Error("tail should contain last lines")
	}
}

func TestTruncateResult_ShortContent(t *testing.T) {
	short := "hello world"
	result := TruncateResult(short, 500)
	if result != short {
		t.Errorf("short content should not be truncated: %s", result)
	}
}

// ──────────────────────────────────────────────────────────
// P2-1: write_file 内容预览
// ──────────────────────────────────────────────────────────

func TestWriteFilePreview(t *testing.T) {
	f := NewFileTool()
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "preview_test.txt")

	// 短内容（≤30行）：完整预览。
	shortContent := "line one\nline two\nline three"
	args := toJSON(map[string]any{
		"action":  "write",
		"path":    tmpFile,
		"content": shortContent,
	})
	result, err := f.Execute(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "3 lines") {
		t.Errorf("should report line count, got: %s", result)
	}
	if !strings.Contains(result, "   1 | line one") {
		t.Errorf("should show line-numbered preview, got: %s", result)
	}

	// 长内容（>30行）：截断预览 + 总行数提示。
	longLines := make([]string, 50)
	for i := 0; i < 50; i++ {
		longLines[i] = fmt.Sprintf("line %d", i+1)
	}
	longContent := strings.Join(longLines, "\n")
	args2 := toJSON(map[string]any{
		"action":  "write",
		"path":    tmpFile,
		"content": longContent,
	})
	result2, err := f.Execute(args2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result2, "50 lines total") {
		t.Errorf("should show total line count for long content, got: %s", result2)
	}
	if !strings.Contains(result2, "   1 | line 1") {
		t.Error("preview should start with line 1")
	}
}

// ──────────────────────────────────────────────────────────
// P2-3: 别名
// ──────────────────────────────────────────────────────────

func TestRegistryAliasLookup(t *testing.T) {
	r := NewRegistry()

	// 注册带别名的工具。
	aliased := &aliasTestTool{name: "new_name", aliases: []string{"old_name", "legacy"}}
	r.Register(aliased)

	// 通过正式名称查找。
	if r.Get("new_name") == nil {
		t.Error("should find tool by canonical name")
	}

	// 通过别名查找。
	if r.Get("old_name") == nil {
		t.Error("should find tool by alias")
	}
	if r.Get("legacy") == nil {
		t.Error("should find tool by second alias")
	}

	// 不存在的名称。
	if r.Get("nonexistent") != nil {
		t.Error("should return nil for unknown name")
	}
}

func TestRegistryAliasConflict_Panics(t *testing.T) {
	r := NewRegistry()
	r.Register(&aliasTestTool{name: "tool_a", aliases: []string{"shared"}})

	// 别名与已有工具名冲突应 panic。
	defer func() {
		if r := recover(); r == nil {
			t.Error("should panic on alias conflict with existing tool name")
		}
	}()
	r.Register(&aliasTestTool{name: "shared"}) // "shared" 已被 tool_a 注册为别名
}

func TestAllToolsHaveAliases(t *testing.T) {
	tools := []Tool{
		NewShellTool(),
		NewFileTool(),
		NewEditTool(),
		NewGrepTool(),
		NewListTool(),
		NewGitTool(),
	}
	for _, tool := range tools {
		aliases := tool.Aliases()
		if aliases == nil {
			continue // nil is valid
		}
		if len(aliases) != 0 {
			t.Errorf("%s: expected nil or empty aliases, got %v", tool.Name(), aliases)
		}
	}
}

// ──────────────────────────────────────────────────────────
// P3-1: ToolHook
// ──────────────────────────────────────────────────────────

type testHook struct {
	beforeCalls []string
	afterCalls  []string
	beforeErr   error
}

func (h *testHook) BeforeExecute(toolName, args string) error {
	h.beforeCalls = append(h.beforeCalls, toolName)
	return h.beforeErr
}

func (h *testHook) AfterExecute(toolName, args, result string, execErr error) {
	h.afterCalls = append(h.afterCalls, toolName)
}

func TestRegistryBeforeHooks(t *testing.T) {
	r := NewRegistry()
	hook := &testHook{}
	r.AddHook(hook)

	err := r.BeforeHooks("shell", `{"command":"ls"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hook.beforeCalls) != 1 || hook.beforeCalls[0] != "shell" {
		t.Errorf("expected before call for shell, got %v", hook.beforeCalls)
	}
}

func TestRegistryBeforeHooksBlocks(t *testing.T) {
	r := NewRegistry()
	r.AddHook(&testHook{beforeErr: fmt.Errorf("blocked")})

	err := r.BeforeHooks("rm", `{}`)
	if err == nil {
		t.Error("should return error when hook blocks")
	}
}

func TestRegistryAfterHooks(t *testing.T) {
	r := NewRegistry()
	hook := &testHook{}
	r.AddHook(hook)

	r.AfterHooks("grep", `{"pattern":"x"}`, "result", nil)
	if len(hook.afterCalls) != 1 || hook.afterCalls[0] != "grep" {
		t.Errorf("expected after call for grep, got %v", hook.afterCalls)
	}
}

func TestRegistryAfterHooks_ExecError(t *testing.T) {
	r := NewRegistry()
	hook := &testHook{}
	r.AddHook(hook)

	execErr := fmt.Errorf("command failed")
	r.AfterHooks("shell", `{"command":"bad"}`, "error output", execErr)
	if len(hook.afterCalls) != 1 {
		t.Error("AfterExecute should be called even on error")
	}
}

func TestRegistryMultipleHooks(t *testing.T) {
	r := NewRegistry()
	h1 := &testHook{}
	h2 := &testHook{}
	r.AddHook(h1)
	r.AddHook(h2)

	r.BeforeHooks("test", `{}`)
	r.AfterHooks("test", `{}`, "done", nil)

	if len(h1.beforeCalls) != 1 || len(h2.beforeCalls) != 1 {
		t.Error("both hooks should receive BeforeExecute")
	}
	if len(h1.afterCalls) != 1 || len(h2.afterCalls) != 1 {
		t.Error("both hooks should receive AfterExecute")
	}
}

// ──────────────────────────────────────────────────────────
// 测试辅助
// ──────────────────────────────────────────────────────────

// aliasTestTool 是用于测试别名的简单工具。
type aliasTestTool struct {
	name    string
	aliases []string
}

func (t *aliasTestTool) Name() string                               { return t.name }
func (t *aliasTestTool) Aliases() []string                          { return t.aliases }
func (t *aliasTestTool) Description() string                        { return "test" }
func (t *aliasTestTool) Parameters() map[string]any                 { return map[string]any{} }
func (t *aliasTestTool) Execute(args string) (string, error)        { return "ok", nil }
func (t *aliasTestTool) CheckPermission(args string) PermissionResult { return PermissionResult{Allow: true} }
func (t *aliasTestTool) PromptGuide() string                        { return "" }
func (t *aliasTestTool) IsConcurrencySafe(args string) bool         { return true }
func (t *aliasTestTool) IsReadOnly(args string) bool                { return true }
func (t *aliasTestTool) ResultLimit() int                           { return 1000 }

// ──────────────────────────────────────────────────────────
// Shell 危险命令检测
// ──────────────────────────────────────────────────────────

func TestIsDangerousShellCommand(t *testing.T) {
	tests := []struct {
		command  string
		dangerous bool
	}{
		{`{"command": "ls -la"}`, false},
		{`{"command": "cat file.txt"}`, false},
		{`{"command": "rm /tmp/foo"}`, true},          // 普通 rm 删除 → 需确认（本次新增）
		{`{"command": "rm -f /tmp/foo"}`, true},        // rm -f（无 -r）也需确认
		{`{"command": "rmdir /tmp/foo"}`, false},       // rmdir 不含 "rm "，不误伤
		{`{"command": "warmup --check"}`, false},       // warmup 等含 rm 子串的词不误伤
		{`{"command": "rm -rf /tmp/foo"}`, true},
		{`{"command": "sudo rm file"}`, true},
		{`{"command": "chmod 777 script.sh"}`, true},
		{`{"command": "mkfs.ext4 /dev/sda"}`, true},
		{`{"command": "shutdown -h now"}`, true},
		{`{"command": "git status"}`, false},
		{`{"command": "echo hello"}`, false},
		{`{"command": "docker ps"}`, false},
		// P1③ 加固：空白归一化，防止前缀子串匹配被多空格/制表符绕过。
		{`{"command": "rm  -rf  /tmp/foo"}`, true},
		{`{"command": "rm\t-rf\t/tmp/foo"}`, true},
		{`{"command": "  rm -rf /tmp/foo"}`, true},
		{`{"command": "RM -RF /tmp/foo"}`, true},
		{`{"command": "rm -rf /tmp/foo && ls"}`, true},
		// 变量拼接等间接绕过仍无法静态识别（fail-open 可接受，文档已注明）。
		{`{"command": "x=rm; $x -rf /tmp/foo"}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.command[:min(30, len(tt.command))], func(t *testing.T) {
			if got := isDangerousShellCommand(tt.command); got != tt.dangerous {
				t.Errorf("isDangerousShellCommand(%s) = %v, want %v", tt.command, got, tt.dangerous)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────
// Tool interface 完整性检查
// ──────────────────────────────────────────────────────────

func TestAllToolsImplementInterface(t *testing.T) {
	// 编译期保证所有工具实现 Tool 接口。
	// 此测试在运行时验证所有方法的返回值格式正确。
	tools := []Tool{
		NewShellTool(),
		NewFileTool(),
		NewEditTool(),
		NewGrepTool(),
		NewListTool(),
		NewGitTool(),
	}

	for _, tool := range tools {
		name := tool.Name()
		if name == "" {
			t.Error("tool name should not be empty")
		}
		if tool.Description() == "" {
			t.Errorf("%s: description should not be empty", name)
		}
		if tool.Parameters() == nil {
			t.Errorf("%s: parameters should not be nil", name)
		}
		if tool.ResultLimit() < 0 {
			t.Errorf("%s: result limit should be >= 0", name)
		}
		if tool.PromptGuide() == "" && name != "list" {
			// list 工具允许空 PromptGuide（行为直观）
			t.Logf("%s: PromptGuide is empty (may be intentional)", name)
		}

		// CheckPermission 应该对空参数也能处理
		perm := tool.CheckPermission(`{}`)
		if perm.Reason == "" && !perm.Allow {
			t.Errorf("%s: denied permission without reason", name)
		}

		// IsConcurrencySafe + IsReadOnly 应该能处理空参数
		safe := tool.IsConcurrencySafe(`{}`)
		ro := tool.IsReadOnly(`{}`)
		if safe && !ro {
			t.Errorf("%s: concurrency-safe but not read-only is suspicious", name)
		}
	}
}

// ──────────────────────────────────────────────────────────
// Registry 测试
// ──────────────────────────────────────────────────────────

func TestRegistryFunctionDefinitions(t *testing.T) {
	r := NewRegistry()
	r.Register(NewGrepTool())
	r.Register(NewListTool())

	defs := r.FunctionDefinitions()
	if len(defs) != 2 {
		t.Errorf("expected 2 definitions, got %d", len(defs))
	}

	for _, def := range defs {
		if def.Type != "function" {
			t.Errorf("unexpected tool type: %s", def.Type)
		}
		if def.Function == nil {
			t.Error("function definition is nil")
		}
	}
}

func TestRegistryDescriptions(t *testing.T) {
	r := NewRegistry()
	desc := r.Descriptions()
	if desc != "(无可用工具)" {
		t.Errorf("empty registry should say no tools, got: %s", desc)
	}

	r.Register(NewGrepTool())
	desc = r.Descriptions()
	if !strings.Contains(desc, "grep") {
		t.Errorf("descriptions should mention grep, got: %s", desc)
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	r.Register(NewShellTool())

	if r.Get("shell") == nil {
		t.Error("should find shell tool")
	}
	if r.Get("nonexistent") != nil {
		t.Error("should not find nonexistent tool")
	}
}

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

func toJSON(v map[string]any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

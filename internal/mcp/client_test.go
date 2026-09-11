package mcp

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"tacode/internal/tool"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestMcpToolName(t *testing.T) {
	if got := mcpToolName("fetch", "fetch"); got != "fetch_fetch" {
		t.Errorf("mcpToolName = %q, want fetch_fetch", got)
	}
	if got := mcpToolName("search", "web_search"); got != "search_web_search" {
		t.Errorf("mcpToolName = %q, want search_web_search", got)
	}
}

func TestBuildSchema(t *testing.T) {
	// 构造最小 mcp.Tool 验证 schema 转换。
	mtool := mcp.Tool{
		Name: "fetch",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{"url": map[string]any{"type": "string"}},
			Required:   []string{"url"},
		},
	}
	schema := buildSchema(mtool)
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
	if _, ok := schema["properties"].(map[string]any); !ok {
		t.Error("schema should have properties map")
	}
	req, ok := schema["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "url" {
		t.Errorf("schema required = %v, want [url]", schema["required"])
	}
}

func TestAdapter_Basics(t *testing.T) {
	// 构造 adapter（client 可为 nil——Execute 才用 client）。
	a := &mcpToolAdapter{
		name:        "fetch_fetch",
		description: "Fetch a URL",
		schema:      map[string]any{"type": "object"},
		client:      nil,
		toolName:    "fetch",
	}
	if a.Name() != "fetch_fetch" {
		t.Errorf("Name = %q", a.Name())
	}
	if a.Description() != "Fetch a URL" {
		t.Errorf("Description = %q", a.Description())
	}
	if a.Parameters() == nil {
		t.Error("Parameters should not be nil")
	}
	if a.ResultLimit() != 12000 {
		t.Errorf("ResultLimit = %d", a.ResultLimit())
	}
	// 权限默认放行。
	if !a.CheckPermission(`{}`).Allow {
		t.Error("CheckPermission should default to Allow")
	}
	// 保守串行。
	if a.IsConcurrencySafe(`{}`) {
		t.Error("IsConcurrencySafe should be false")
	}
	if a.IsReadOnly(`{}`) {
		t.Error("IsReadOnly should be false")
	}
}

func TestAdapter_Execute_InvalidArgs(t *testing.T) {
	// client 为 nil，Execute 传无效 JSON → 应返回 parse 错误（不触 client）。
	a := &mcpToolAdapter{client: nil, toolName: "fetch"}
	_, err := a.Execute(`not json`)
	if err == nil {
		t.Fatal("expected error for invalid args JSON")
	}
	if !strings.Contains(err.Error(), "parse mcp args") {
		t.Errorf("error should mention parse, got %v", err)
	}
}

// 集成测试（可选）：真实拉起 mcp-server-fetch，验证工具枚举。
// 无 npx/node 或网络受限时 t.Skip（-short 模式直接跳过，避免拖慢套件）。
func TestConnect_FetchServer_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	mgr := New()
	tools, err := mgr.Connect(ctx, ServerConfig{
		Name:    "fetch",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-fetch"},
	})
	if err != nil {
		t.Skipf("fetch server not available (network?): %v", err)
	}
	defer mgr.Close()

	if len(tools) == 0 {
		t.Error("should discover at least one tool from fetch server")
	}
	found := false
	for _, tt := range tools {
		if tt.Name() == "fetch_fetch" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("should find fetch_fetch tool, got: %v", names(tools))
	}
}

func names(ts []tool.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name())
	}
	return out
}

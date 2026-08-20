# WebFetch Tool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `webfetch` 工具，让 Agent 能抓取网页并以 Markdown + 元信息形式返回内容（仅 GET，无副作用，默认放行）。

**Architecture:** 单文件 `internal/tool/webfetch.go` 实现 `Tool` 接口。`http.Client`（30s 超时 + 5 次重定向上限）请求 URL，按 `Content-Type` 分支处理（HTML→Markdown 转换 + 提取元信息；JSON/Markdown/纯文本原样返回）；元信息从 `<title>` / `<meta name="description">` 提取（fallback `og:title` / `og:description`）；输出格式 `Title: … / Description: … / <Markdown 正文>`。测试在 `internal/tool/webfetch_test.go`，用 `httptest.NewServer` 模拟外部网站。

**Tech Stack:** Go 1.26.4，`net/http`（stdlib）+ `github.com/JohannesKaufmann/html-to-markdown`（新增 1 个依赖）。

**Spec:** `docs/superpowers/specs/2026-08-20-webfetch-tool-design.md`

---

### Task 1: 添加 `html-to-markdown` 依赖

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: 添加依赖**

Run:
```bash
go get github.com/JohannesKaufmann/html-to-markdown/v2
```

Expected: go.mod 出现 `github.com/JohannesKaufmann/html-to-markdown/v2 vX.Y.Z` 一行，go.sum 出现对应 hash。

- [ ] **Step 2: 校验依赖**

Run:
```bash
go mod tidy
```

Expected: 无变更或仅整理间接依赖。

- [ ] **Step 3: 验证编译**

Run:
```bash
go build ./...
```

Expected: 编译成功，无错误。

- [ ] **Step 4: 提交**

```bash
git add go.mod go.sum
git commit -m "feat(webfetch): add html-to-markdown dependency"
```

---

### Task 2: 创建 `internal/tool/webfetch.go` 骨架

**Files:**
- Create: `internal/tool/webfetch.go`

- [ ] **Step 1: 创建 `internal/tool/webfetch.go`，先放完整骨架（常量 + 构造函数 + 10 个接口方法 + 核心辅助函数骨架）**

```go
package tool

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2"
)

const (
	webFetchTimeout     = 30 * time.Second
	webFetchMaxBody     = 5 * 1024 * 1024 // 5MB
	webFetchMaxRedirect = 5
	webFetchMaxChars    = 16000
)

// WebFetchTool 提供网页抓取能力。
//
// 与 shell 调 curl 的区别：
//   - HTML 自动转 Markdown，LLM 上下文更干净
//   - 提取页面元信息（title、description）一并返回
//   - Content-Type 分支：JSON/Markdown/纯文本原样返回
//   - 默认放行（GET 无副作用），无需用户确认
//
// 本版本仅支持 GET，POST/PUT 等写操作不开放。
type WebFetchTool struct {
	client *http.Client
}

// NewWebFetchTool 创建 webfetch 工具。
func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{
		client: &http.Client{
			Timeout: webFetchTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= webFetchMaxRedirect {
					return fmt.Errorf("超过最大重定向次数 (%d)", webFetchMaxRedirect)
				}
				return nil
			},
		},
	}
}

// ── Tool 接口：基础方法 ──

func (t *WebFetchTool) Name() string        { return "webfetch" }
func (t *WebFetchTool) Aliases() []string   { return nil }

func (t *WebFetchTool) Description() string {
	return "通过 HTTP GET 抓取网页内容并转换为 Markdown。支持 HTML 转 Markdown 和 JSON/Markdown/纯文本原样返回，附带页面元信息（title、description）。"
}

func (t *WebFetchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "要获取的 URL（必须包含 http:// 或 https://）",
			},
			"max_chars": map[string]any{
				"type":        "integer",
				"description": "返回内容的最大字符数（默认 16000，与 ResultLimit 对齐；上限也是 16000）",
			},
		},
		"required": []string{"url"},
	}
}

// Execute 执行网页抓取。
func (t *WebFetchTool) Execute(args string) (string, error) {
	var params struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if strings.TrimSpace(params.URL) == "" {
		return "", fmt.Errorf("url is empty")
	}
	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		return "", fmt.Errorf("url must include http:// or https://")
	}

	maxChars := webFetchMaxChars
	if params.MaxChars > 0 && params.MaxChars < maxChars {
		maxChars = params.MaxChars
	}

	return t.fetch(params.URL, maxChars)
}

// fetch 拉取 URL 并转换为 Markdown + 元信息。
func (t *WebFetchTool) fetch(url string, maxChars int) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("invalid request: %w", err)
	}
	req.Header.Set("User-Agent", "agentic-webfetch/1.0")

	resp, err := t.client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "超过最大重定向次数") {
			return "", err
		}
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	// 限制最大响应体大小。
	body, err := readBounded(resp.Body, webFetchMaxBody)
	if err != nil {
		return "", err
	}

	contentType := resp.Header.Get("Content-Type")
	return t.convertAndFormat(contentType, body, maxChars)
}

// convertAndFormat 按 Content-Type 分支处理并拼装输出。
func (t *WebFetchTool) convertAndFormat(contentType string, body []byte, maxChars int) (string, error) {
	ct := strings.ToLower(contentType)

	switch {
	case strings.Contains(ct, "text/html"):
		html := string(body)
		title, description := extractMetadata(html)
		md, err := htmltomarkdown.Convert(html)
		if err != nil {
			return "", fmt.Errorf("HTML 转 Markdown 失败: %w", err)
		}
		return formatOutput(title, description, md, maxChars), nil
	case strings.Contains(ct, "application/json"),
		strings.Contains(ct, "text/plain"),
		strings.Contains(ct, "text/markdown"):
		return truncate(string(body), maxChars), nil
	default:
		return "", fmt.Errorf("不支持的 Content-Type: %s（仅支持 text/html, application/json, text/plain, text/markdown）", contentType)
	}
}

// ── Tool 接口：权限内聚 ──

// CheckPermission webfetch 是 GET 只读操作，默认放行。
func (t *WebFetchTool) CheckPermission(args string) PermissionResult {
	return PermissionResult{Allow: true}
}

// ── Tool 接口：Prompt 自引导 ──

func (t *WebFetchTool) PromptGuide() string {
	return "用于抓取网页内容（HTML 转 Markdown）。仅支持 GET 写操作（如 POST）不接受。" +
		"返回内容包含 Title/Description 元信息 + Markdown 正文。" +
		"如果页面过大（>5MB）会报错，可考虑使用更具体的 URL 或 docs/链接。" +
		"超时 30s，重定向最多 5 次。"
}

// ── Tool 接口：并发安全 ──

func (t *WebFetchTool) IsConcurrencySafe(args string) bool { return true }
func (t *WebFetchTool) IsReadOnly(args string) bool        { return true }

// ── Tool 接口：结果上限 ──

// ResultLimit 最大返回 16000 字符。
func (t *WebFetchTool) ResultLimit() int { return webFetchMaxChars }

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// readBounded 读取 body 但不超过 maxBytes 字节。
func readBounded(body interface {
	Read([]byte) (int, error)
}, maxBytes int) ([]byte, error) {
	// 使用 io.LimitReader 限制读取大小。
	// 实现见 Task 3。
	return nil, fmt.Errorf("not implemented")
}

// extractMetadata 从 HTML 中提取 title 和 description。
func extractMetadata(html string) (title, description string) {
	// 实现见 Task 3。
	return "", ""
}

// formatOutput 拼装最终输出。
func formatOutput(title, description, body string, maxChars int) string {
	// 实现见 Task 3。
	return body
}

// truncate 截断字符串至 maxChars（保留前 70% + 后 30%，加截断提示）。
func truncate(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	head := int(float64(maxChars) * 0.7)
	tail := maxChars - head
	const marker = "\n\n…(已截断)…\n\n"
	if head+tail+len(marker) > maxChars {
		tail = maxChars - head - len(marker)
		if tail < 0 {
			tail = 0
		}
	}
	return s[:head] + marker + s[len(s)-tail:]
}
```

- [ ] **Step 2: 验证骨架可编译**

Run:
```bash
go build ./internal/tool/
```

Expected: 编译失败（提示 `not implemented`、`undefined: htmltomarkdown` 等）。这是预期的，因为 `htmltomarkdown` 是 v2 包的导入路径，骨架用的是 `Convert` 函数。

- [ ] **Step 3: 验证骨架编译（修正导入路径）**

先实际确认一下 v2 包的 API：

```bash
go doc github.com/JohannesKaufmann/html-to-markdown/v2 2>&1 | head -30
```

如果 `Convert` 不存在，按 go doc 输出调整（v2 实际 API 可能为 `converter.NewConverter().ConvertString(...)`）。具体调整在 Task 3 之前完成。

- [ ] **Step 4: 提交骨架**

```bash
git add internal/tool/webfetch.go
git commit -m "feat(webfetch): add WebFetchTool skeleton with Tool interface"
```

---

### Task 3: 实现辅助函数（readBounded / extractMetadata / formatOutput）

**Files:**
- Modify: `internal/tool/webfetch.go`

- [ ] **Step 1: 写失败测试（`internal/tool/webfetch_test.go`）**

```go
package tool

import (
	"strings"
	"testing"
)

func TestExtractMetadata_Title(t *testing.T) {
	html := `<html><head><title>Hello World</title></head><body></body></html>`
	title, desc := extractMetadata(html)
	if title != "Hello World" {
		t.Errorf("title = %q, want %q", title, "Hello World")
	}
	if desc != "" {
		t.Errorf("description = %q, want empty", desc)
	}
}

func TestExtractMetadata_DescriptionMeta(t *testing.T) {
	html := `<html><head>
		<title>Title</title>
		<meta name="description" content="A description">
	</head><body></body></html>`
	title, desc := extractMetadata(html)
	if title != "Title" {
		t.Errorf("title = %q, want %q", title, "Title")
	}
	if desc != "A description" {
		t.Errorf("description = %q, want %q", desc, "A description")
	}
}

func TestExtractMetadata_OGFallback(t *testing.T) {
	html := `<html><head>
		<meta property="og:title" content="OG Title">
		<meta property="og:description" content="OG Description">
	</head><body></body></html>`
	title, desc := extractMetadata(html)
	if title != "OG Title" {
		t.Errorf("title = %q, want %q", title, "OG Title")
	}
	if desc != "OG Description" {
		t.Errorf("description = %q, want %q", desc, "OG Description")
	}
}

func TestExtractMetadata_Missing(t *testing.T) {
	html := `<html><body>No metadata</body></html>`
	title, desc := extractMetadata(html)
	if title != "" || desc != "" {
		t.Errorf("got (%q, %q), want both empty", title, desc)
	}
}

func TestFormatOutput_TitleAndDescription(t *testing.T) {
	out := formatOutput("My Title", "My Desc", "Body content", 16000)
	if !strings.HasPrefix(out, "Title: My Title\nDescription: My Desc\n\n") {
		t.Errorf("output fake prefix: %q", out[:50])
	}
	if !strings.Contains(out, "Body content") {
		t.Errorf("output missing body: %q", out)
	}
}

func TestFormatOutput_OnlyBody(t *testing.T) {
	out := formatOutput("", "", "Body only", 16000)
	if out != "Body only" {
		t.Errorf("output = %q, want %q", out, "Body only")
	}
}

func TestFormatOutput_OnlyTitle(t *testing.T) {
	out := formatOutput("Only Title", "", "Body", 16000)
	if !strings.HasPrefix(out, "Title: Only Title\n\n") {
		t.Errorf("output wrong prefix: %q", out)
	}
}

func TestFormatOutput_Truncate(t *testing.T) {
	body := strings.Repeat("a", 100)
	out := formatOutput("", "", body, 50)
	if !strings.Contains(out, "已截断") {
		t.Errorf("expected truncation marker, got: %q", out)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
go test ./internal/tool/ -run TestExtractMetadata -v
```

Expected: FAIL（`extractMetadata` 未实现）。

- [ ] **Step 3: 实现 `extractMetadata`（用 `golang.org/x/net/html` 解析）**

首先添加库依赖：

```bash
go get golang.org/x/net/html
```

替换 `internal/tool/webfetch.go` 中的 `extractMetadata`、`formatOutput`、`readBounded` 函数：

```go
import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
)

// ... (其他保留)

func readBounded(body io.Reader, maxBytes int) ([]byte, error) {
	limited := io.LimitReader(body, int64(maxBytes)+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if len(buf) > maxBytes {
		return nil, fmt.Errorf("响应过大 (>%d MB)，超过 5MB 限制", maxBytes/(1024*1024))
	}
	return buf, nil
}

// extractMetadata 从 HTML 中提取 title 和 description。
//
// 优先级：title 用 <title>（fallback og:title）；
// description 用 <meta name="description">（fallback og:description）。
func extractMetadata(htmlStr string) (title, description string) {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return "", ""
	}

	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "title":
				if title == "" && n.FirstChild != nil {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				name := strings.ToLower(getAttr(n, "name"))
				property := strings.ToLower(getAttr(n, "property"))
				content := strings.TrimSpace(getAttr(n, "content"))
				if description == "" {
					if name == "description" && content != "" {
						description = content
					} else if property == "og:description" && content != "" {
						description = content
					}
				}
				if title == "" && property == "og:title" && content != "" {
					title = content
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	return title, description
}

// getAttr 获取 HTML 节点的属性值（不区分大小写）。
func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// formatOutput 拼装最终输出。
func formatOutput(title, description, body string, maxChars int) string {
	var sb strings.Builder
	hasMeta := false
	if title != "" {
		sb.WriteString("Title: ")
		sb.WriteString(title)
		sb.WriteByte('\n')
		hasMeta = true
	}
	if description != "" {
		sb.WriteString("Description: ")
		sb.WriteString(description)
		sb.WriteByte('\n')
		hasMeta = true
	}
	if hasMeta {
		sb.WriteString("\n")
	}
	sb.WriteString(body)
	return truncate(sb.String(), maxChars)
}

// truncate 截断字符串（保留前 70% + 后 30%，加截断提示）。
func truncate(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	head := int(float64(maxChars) * 0.7)
	tail := maxChars - head
	const marker = "\n\n…(已截断)…\n\n"
	if head+len(marker)+tail > maxChars {
		tail = maxChars - head - len(marker)
		if tail < 0 {
			tail = 0
		}
	}
	if head+tail > len(s) {
		return s
	}
	return s[:head] + marker + s[len(s)-tail:]
}
```

- [ ] **Step 4: 实现 `formatOutput`（在上面已包含）**

- [ ] **Step 5: 运行测试验证通过**

Run:
```bash
go test ./internal/tool/ -run TestExtractMetadata -v
go test ./internal/tool/ -run TestFormatOutput -v
```

Expected: 全部 PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/tool/webfetch.go internal/tool/webfetch_test.go go.mod go.sum
git commit -m "feat(webfetch): implement extractMetadata and formatOutput"

# (如果 formatOutput 和 extractMetadata 函数在两个不同的 commit 中更清晰，
# 可以拆分。当前任务合并为一个 commit 更顺畅。)
```

---

### Task 4: 实现 HTML→Markdown 转换 + Content-Type 分支

**Files:**
- Modify: `internal/tool/webfetch.go`
- Modify: `internal/tool/webfetch_test.go`

- [ ] **Step 1: 写失败测试**

在 `webfetch_test.go` 末尾追加：

```go
import (
	"net/http"
	"net/http/httptest"
	// ... 保留既有 import
)

func TestConvertAndFormat_HTML(t *testing.T) {
	tl := NewWebFetchTool()
	html := `<html><head><title>T</title></head><body><h1>Hello</h1><p>World</p></body></html>`
	out, err := tl.convertAndFormat("text/html; charset=utf-8", []byte(html), 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Title: T") {
		t.Errorf("missing title: %q", out)
	}
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "World") {
		t.Errorf("missing body: %q", out)
	}
	// html-to-markdown 默认不输出 HTML 标签
	if strings.Contains(out, "<h1>") || strings.Contains(out, "<p>") {
		t.Errorf("raw HTML tags not converted: %q", out)
	}
}

func TestConvertAndFormat_HTML_StripScript(t *testing.T) {
	tl := NewWebFetchTool()
	html := `<html><head><title>Page</title>
		<script>alert('xss')</script>
	</head><body><p>visible text</p></body></html>`
	out, err := tl.convertAndFormat("text/html", []byte(html), 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "alert(") || strings.Contains(out, "<script>") {
		t.Errorf("script not stripped: %q", out)
	}
	if !strings.Contains(out, "visible text") {
		t.Errorf("missing body: %q", out)
	}
}

func TestConvertAndFormat_JSON(t *testing.T) {
	tl := NewWebFetchTool()
	body := `{"hello": "world"}`
	out, err := tl.convertAndFormat("application/json", []byte(body), 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != body {
		t.Errorf("output = %q, want %q", out, body)
	}
}

func TestConvertAndFormat_PlainText(t *testing.T) {
	tl := NewWebFetchTool()
	body := "just plain text"
	out, err := tl.convertAndFormat("text/plain", []byte(body), 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != body {
		t.Errorf("output = %q, want %q", out, body)
	}
}

func TestConvertAndFormat_Markdown(t *testing.T) {
	tl := NewWebFetchTool()
	body := "# Heading\n\ntext"
	out, err := tl.convertAndFormat("text/markdown", []byte(body), 16000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != body {
		t.Errorf("output = %q, want %q", out, body)
	}
}

func TestConvertAndFormat_UnsupportedContentType(t *testing.T) {
	tl := NewWebFetchTool()
	_, err := tl.convertAndFormat("image/png", []byte{0x89, 0x50}, 16000)
	if err == nil {
		t.Error("expected error for unsupported content-type")
	}
	if !strings.Contains(err.Error(), "不支持的 Content-Type") {
		t.Errorf("error message wrong: %v", err)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
go test ./internal/tool/ -run TestConvertAndFormat -v
```

Expected: FAIL（`convertAndFormat` 未实现或 `htmltomarkdown` 包路径不对）。

- [ ] **Step 3: 确认 html-to-markdown v2 API**

```bash
go doc github.com/JohannesKaufmann/html-to-markdown/v2
```

v2 API 大致是：
- `htmltomarkdown.Convert(html string, opts ...Option) (string, error)`
- 选项如 `htmltomarkdown.WithDomain("...")` 用于相对路径

如果 v2 API 不同，按 go doc 输出调整 `convertAndFormat` 中的调用方式。

- [ ] **Step 4: 实现 `convertAndFormat`（已在 Task 2 的骨架中给出）**

骨架中的 `convertAndFormat` 已实现。如果编译失败，调整 `htmltomarkdown.Convert(...)` 调用为正确的 API。

- [ ] **Step 5: 运行测试验证通过**

Run:
```bash
go test ./internal/tool/ -run TestConvertAndFormat -v
```

Expected: 全部 PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/tool/webfetch.go internal/tool/webfetch_test.go
git commit -m "feat(webfetch): implement HTML→Markdown conversion and content-type routing"
```

---

### Task 5: 实现 HTTP 请求 + 错误处理（端到端）

**Files:**
- Modify: `internal/tool/webfetch_test.go`

- [ ] **Step 1: 写失败测试**

在 `webfetch_test.go` 末尾追加：

```go
func TestExecute_HTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Test</title></head><body><h1>Hi</h1></body></html>`)
	}))
	defer server.Close()

	tl := NewWebFetchTool()
	out, err := tl.Execute(fmt.Sprintf(`{"url": %q}`, server.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Title: Test") {
		t.Errorf("missing title: %q", out)
	}
	if !strings.Contains(out, "Hi") {
		t.Errorf("missing body: %q", out)
	}
}

func TestExecute_EmptyURL(t *testing.T) {
	tl := NewWebFetchTool()
	_, err := tl.Execute(`{"url": ""}`)
	if err == nil {
		t.Error("expected error for empty url")
	}
}

func TestExecute_InvalidURL(t *testing.T) {
	tl := NewWebFetchTool()
	_, err := tl.Execute(`{"url": "example.com"}`)
	if err == nil || !strings.Contains(err.Error(), "http://") {
		t.Errorf("expected scheme error, got: %v", err)
	}
}

func TestExecute_404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	tl := NewWebFetchTool()
	_, err := tl.Execute(fmt.Sprintf(`{"url": %q}`, server.URL))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got: %v", err)
	}
}

func TestExecute_500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	tl := NewWebFetchTool()
	_, err := tl.Execute(fmt.Sprintf(`{"url": %q}`, server.URL))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got: %v", err)
	}
}

func TestExecute_Redirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "redirected")
	}))
	defer final.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer server.Close()

	tl := NewWebFetchTool()
	out, err := tl.Execute(fmt.Sprintf(`{"url": %q}`, server.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "redirected" {
		t.Errorf("output = %q, want %q", out, "redirected")
	}
}

func TestExecute_TooManyRedirects(t *testing.T) {
	// 构造循环重定向：A → B → A
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverB.URL, http.StatusFound)
	}))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverA.URL, http.StatusFound)
	}))
	defer serverB.Close()

	tl := NewWebFetchTool()
	_, err := tl.Execute(fmt.Sprintf(`{"url": %q}`, serverA.URL))
	if err == nil || !strings.Contains(err.Error(), "重定向") {
		t.Errorf("expected redirect error, got: %v", err)
	}
}

func TestExecute_LargeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// 写 6MB 数据
		buf := make([]byte, 6*1024*1024)
		w.Write(buf)
	}))
	defer server.Close()

	tl := NewWebFetchTool()
	_, err := tl.Execute(fmt.Sprintf(`{"url": %q}`, server.URL))
	if err == nil || !strings.Contains(err.Error(), "过大") {
		t.Errorf("expected large response error, got: %v", err)
	}
}

func TestExecute_MaxChars(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("a", 1000))
	}))
	defer server.Close()

	tl := NewWebFetchTool()
	out, err := tl.Execute(fmt.Sprintf(`{"url": %q, "max_chars": 100}`, server.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "已截断") {
		t.Errorf("expected truncation marker, got len=%d", len(out))
	}
	if len(out) > 200 {
		t.Errorf("output too long: %d chars", len(out))
	}
}

func TestExecute_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(35 * time.Second)
	}))
	defer server.Close()

	tl := NewWebFetchTool()
	_, err := tl.Execute(fmt.Sprintf(`{"url": %q}`, server.URL))
	if err == nil {
		t.Error("expected timeout error")
	}
}
```

注意：最后这个 timeout 测试需要 30 秒才能跑完。可以选择：
- 在 `go test` 命令里加 `-short` 跳过 timeout 测试
- 或在测试里加 `if testing.Short() { t.Skip("skipping timeout test") }`

```go
func TestExecute_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}
	// ... (其余同上)
}
```

在 `webfetch_test.go` 头部 import 块补充 `"time"` 和 `"testing"`。

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
go test ./internal/tool/ -run TestExecute -v -short
```

Expected: 部分 FAIL（未实现的部分）。

- [ ] **Step 3: 实现 `Execute` + `fetch`（骨架已实现大部分）**

骨架已经实现了 `Execute` 和 `fetch`。如果上面的测试 FAIL，检查：
- `Execute` 解析参数、校验 URL、调用 `fetch`
- `fetch` 发请求、检查状态码、读 body、调用 `convertAndFormat`
- 重定向处理在 `http.Client.CheckRedirect` 中

如有问题，按测试期望补全。

- [ ] **Step 4: 运行测试验证通过**

Run:
```bash
go test ./internal/tool/ -run TestExecute -v -short
```

Expected: 全部 PASS（timeout 测试在 short 模式跳过）。

- [ ] **Step 5: 提交**

```bash
git add internal/tool/webfetch.go internal/tool/webfetch_test.go
git commit -m "test(webfetch): add end-to-end HTTP fetch tests"
```

---

### Task 6: 接口方法测试（权限 / 并发 / 结果上限）

**Files:**
- Modify: `internal/tool/webfetch_test.go`

- [ ] **Step 1: 写测试**

```go
func TestWebFetch_Name(t *testing.T) {
	tl := NewWebFetchTool()
	if tl.Name() != "webfetch" {
		t.Errorf("Name() = %q, want %q", tl.Name(), "webfetch")
	}
}

func TestWebFetch_Description(t *testing.T) {
	tl := NewWebFetchTool()
	desc := tl.Description()
	if desc == "" {
		t.Error("Description() is empty")
	}
	if !strings.Contains(desc, "Markdown") {
		t.Errorf("Description should mention Markdown: %q", desc)
	}
}

func TestWebFetch_Parameters(t *testing.T) {
	tl := NewWebFetchTool()
	params := tl.Parameters()
	if params["type"] != "object" {
		t.Errorf("Parameters type = %v, want object", params["type"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties not a map")
	}
	if _, ok := props["url"]; !ok {
		t.Error("missing url parameter")
	}
}

func TestWebFetch_CheckPermission(t *testing.T) {
	tl := NewWebFetchTool()
	args := []string{
		`{"url": "https://example.com"}`,
		`{"url": "http://localhost:8080"}`,
		``,
		`{invalid json`,
	}
	for _, arg := range args {
		result := tl.CheckPermission(arg)
		if !result.Allow {
			t.Errorf("CheckPermission(%q) = Allow=false, want true", arg)
		}
	}
}

func TestWebFetch_PromptGuide(t *testing.T) {
	tl := NewWebFetchTool()
	guide := tl.PromptGuide()
	if guide == "" {
		t.Error("PromptGuide() is empty")
	}
}

func TestWebFetch_Concurrency(t *testing.T) {
	tl := NewWebFetchTool()
	if !tl.IsConcurrencySafe(`{"url": "https://example.com"}`) {
		t.Error("IsConcurrencySafe() should be true")
	}
	if !tl.IsReadOnly(`{"url": "https://example.com"}`) {
		t.Error("IsReadOnly() should be true")
	}
}

func TestWebFetch_ResultLimit(t *testing.T) {
	tl := NewWebFetchTool()
	if tl.ResultLimit() != 16000 {
		t.Errorf("ResultLimit() = %d, want 16000", tl.ResultLimit())
	}
}
```

- [ ] **Step 2: 运行测试**

Run:
```bash
go test ./internal/tool/ -run TestWebFetch_Name -v
go test ./internal/tool/ -run TestWebFetch_Description -v
go test ./internal/tool/ -run TestWebFetch_Parameters -v
go test ./internal/tool/ -run TestWebFetch_CheckPermission -v
go test ./internal/tool/ -run TestWebFetch_PromptGuide -v
go test ./internal/tool/ -run TestWebFetch_Concurrency -v
go test ./internal/tool/ -run TestWebFetch_ResultLimit -v
```

Expected: 全部 PASS（这些是已有骨架支持的）。

- [ ] **Step 3: 提交**

```bash
git add internal/tool/webfetch_test.go
git commit -m "test(webfetch): cover Tool interface methods"
```

---

### Task 7: 在 `main.go` 注册工具

**Files:**
- Modify: `main.go`

- [ ] **Step 1: 注册 `WebFetchTool`**

在 `main.go` 的工具注册块（grep 位置：`tools.Register(tool.NewGitTool())`）后追加：

```go
tools.Register(tool.NewWebFetchTool())
```

注：放在 `NewGitTool()` 之后，与其他工具注册保持一致。

- [ ] **Step 2: 验证编译**

Run:
```bash
go build ./...
```

Expected: 编译成功。

- [ ] **Step 3: 提交**

```bash
git add main.go
git commit -m "feat(main): register webfetch tool"
```

---

### Task 8: 更新 `CLAUDE.md`

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 在 Built-in tools 列表加 `webfetch`**

找到 "Built-in tools" 一节（"Shell: ... Edit: ... Git: ..."），在其末尾追加：

```markdown
- WebFetch: 抓取网页内容（HTML→Markdown + 提取 title/description），仅 GET，默认放行。
```

参考其他工具的描述风格：`(react/tool/webfetch.go)` 是文件路径，`webfetch` 是工具名。

具体在 `Built-in tools` 一节后追加：

```
- WebFetch: 抓取网页内容（HTML→Markdown + 提取 title/description），仅 GET，默认放行。
```

- [ ] **Step 2: 同步更新 File Map（如有 webfetch 相关条目）**

`File Map` 不需更新（`internal/tool/` 列举所属文件，git_test.go 在那里；webfetch.go 在那里）。

- [ ] **Step 3: 提交**

```bash
git add CLAUDE.md
git commit -m "docs: add webfetch tool to built-in tools list"
```

---

### Task 9: 最终验证

**Files:** （无变更）

- [ ] **Step 1: 运行所有测试**

Run:
```bash
go test ./... -short
```

Expected: 全部 PASS。（`-short` 跳过 30s 超时测试。）

- [ ] **Step 2: 完整运行（可选，含 timeout 测试）**

```bash
go test ./... -timeout 60s
```

Expected: 全部 PASS。

- [ ] **Step 3: `go vet` 检查**

```bash
go vet ./...
```

Expected: 无警告。

- [ ] **Step 4: 查看 git 日志**

```bash
git log --oneline -10
```

Expected: 看到本次的 7-8 个 commits（dep + skeleton + 三个 feature + main + docs）。

- [ ] **Step 5: 手动 sanity check（可选）**

```bash
go build -o agentic .
./agentic -one-shot "用 webfetch 工具抓取 https://example.com 并返回"
```

Expected: 输出 example.com 的 title + body 摘要。

---

## 完成标准

- `go build ./...` 通过
- `go test ./...` 全绿
- `git log` 显示分阶段 commits（依赖、骨架、特性、测试、注册、文档）
- `webfetch` 工具在 REPL 中可调用，能抓取 HTML 页面并返回 Markdown + 元信息

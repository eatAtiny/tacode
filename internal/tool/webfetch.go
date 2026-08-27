package tool

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
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

func (t *WebFetchTool) Name() string      { return "webfetch" }
func (t *WebFetchTool) Aliases() []string { return nil }

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
		return "", err
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
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
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
		md, err := htmltomarkdown.ConvertString(html)
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
	return "用于抓取网页内容（HTML 转 Markdown）。仅支持 GET，写操作（如 POST）不接受。" +
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
func readBounded(body io.Reader, maxBytes int) ([]byte, error) {
	limited := io.LimitReader(body, int64(maxBytes)+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if len(buf) > maxBytes {
		return nil, fmt.Errorf("响应过大 (超过 %d MB 限制)", maxBytes/(1024*1024))
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
// 仅处理超长输入（len(s) > maxChars），此时 head+tail 必小于 len(s)。
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
	return s[:head] + marker + s[len(s)-tail:]
}

# WebFetch 工具设计文档

- 日期：2026-08-20
- 状态：已批准
- 范围：`internal/tool/webfetch.go` 新增 WebFetchTool + `internal/tool/webfetch_test.go` 单元测试 + `main.go` 注册 + `go.mod` 依赖 + `CLAUDE.md` 文档

## 背景

Agent 目前只能通过 `shell` 工具调用 `curl`/`wget` 抓取网页，输出原始 HTML/文本，LLM 难以消化。需要一个内置网页抓取工具，把 HTML 转成结构化 Markdown 并提取页面元信息，行为对齐 Claude Code 的 `WebFetch` 工具。

## 决策

| 决策点 | 结论 |
|--------|------|
| 工具名 | `webfetch`（对齐 Claude Code 命名） |
| 新增依赖 | `github.com/JohannesKaufmann/html-to-markdown` |
| HTTP 方法 | 仅 GET |
| 权限策略 | 默认放行（GET 是只读操作，无副作用） |
| 输出格式 | `Title` + `Description` 元信息 + Markdown 正文 |
| 超时 | 30s（与 shell 对齐） |
| 最大响应体 | 5MB（防止意外下载大文件） |
| 重定向 | 跟随最多 5 次 |
| 结果上限 | `ResultLimit` = 16000 字符（与 shell 对齐） |
| 元数据提取 | `<title>` 优先 + `<meta name="description">` + `<meta property="og:title/description">` |

## 架构

新文件 `internal/tool/webfetch.go`，实现 `Tool` 接口（10 个方法）+ 辅助函数：

```
WebFetchTool
  ├─ Execute(args)        → 解析 {url, max_chars} → fetch → 转换
  ├─ fetchURL(url)       → http.Client 发请求 → *http.Response
  ├─ convertBody(resp)    → 按 Content-Type 分支处理
  │     ├─ text/html      → html-to-markdown + extractMetadata
  │     ├─ text/markdown  → 原样返回
  │     ├─ text/plain     → 原样返回
  │     ├─ application/json → 原样返回
  │     └─ 其他           → 报错
  ├─ extractMetadata(html) → 提取 title + description
  └─ formatOutput(meta, body) → 拼装最终输出
```

无状态工具（无 sandbox 绑定），单实例可全局共享，每次请求独立 http.Client。

## 参数 Schema

```json
{
  "url": {
    "type": "string",
    "description": "要获取的 URL（必须包含 http:// 或 https://）"
  },
  "max_chars": {
    "type": "integer",
    "description": "返回内容的最大字符数（默认 50000，超出会用 ResultLimit 截断）"
  }
}
```

`url` 必填；`max_chars` 可选，不填则用 `ResultLimit`。

## 输出格式

```
Title: <title>
Description: <meta description>

<Markdown 正文>
```

- 元信息缺失对应字段时省略
- 完全没有元信息时只输出 Markdown 正文（无空行）
- JSON / Markdown / 纯文本 content-type：跳过元信息提取，原样返回 body

## 边界处理

| 场景 | 行为 |
|------|------|
| `url` 为空 | 报错：`url is empty` |
| `url` 不含 scheme | 报错：`url must include http:// or https://` |
| 非 200 状态码 | 报错：包含状态码 + 状态文本 + 响应体前 500 字符 |
| 网络错误（DNS / 连接拒绝） | 返回 Go 错误（让 LLM 看到具体原因） |
| 超时（30s） | 报错：`请求超时 (30s)` |
| 响应体 > 5MB | 报错：`响应过大 (X MB)，超过 5MB 限制` |
| content-type 非文本（image / binary） | 报错：`不支持的 Content-Type: <type>`，提示 LLM 这不是文本内容 |
| 重定向 > 5 次 | 报错：`超过最大重定向次数` |

## 权限

`CheckPermission` 对所有 args 都返回 `PermissionResult{Allow: true}`：
- GET 是只读操作，无副作用
- 用户已选默认放行，避免每次抓页面都打断
- 写操作（POST/PUT 等）本版本不开放

## 并发安全

- `IsConcurrencySafe` → `true`：GET 无副作用
- `IsReadOnly` → `true`：GET 无副作用
- 多个 `webfetch` 调用可与其他只读工具并行

## 错误处理

请求链路失败时返回 Go error（`Errorf` 包装），错误信息中文为主，保留原始 err：

```go
resp, err := client.Do(req)
if err != nil {
    if ctx.Err() == context.DeadlineExceeded {
        return "", fmt.Errorf("请求超时 (30s)")
    }
    return "", fmt.Errorf("请求失败: %w", err)
}
defer resp.Body.Close()
if resp.StatusCode != 200 {
    return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.StatusText)
}
```

## 测试计划（`internal/tool/webfetch_test.go`）

用 `httptest.NewServer` 模拟外部网站：

| 测试 | 覆盖点 |
|------|--------|
| `TestWebFetchHTML` | 简单 HTML 页面 → Markdown 转换 + 元信息提取 |
| `TestWebFetchMetadata` | 仅 `<title>` / `<meta description>` / `og:*` 各种组合 |
| `TestWebFetchPlainText` | `text/plain` content-type → 原样返回 |
| `TestWebFetchJSON` | `application/json` content-type → 原样返回 |
| `TestWebFetchMarkdown` | `text/markdown` content-type → 原样返回 |
| `TestWebFetch404` | HTTP 404 → 返回错误 |
| `TestWebFetch500` | HTTP 500 → 返回错误 |
| `TestWebFetchTimeout` | handler sleep 35s → 30s 后报错 |
| `TestWebFetchContentType` | `image/png` → 报错 |
| `TestWebFetchRedirect` | 302 重定向 → 跟随到目标 |
| `TestWebFetchTooManyRedirects` | 6 次重定向 → 报错 |
| `TestWebFetchLargeResponse` | body > 5MB → 报错 |
| `TestWebFetchMaxChars` | 短 max_chars → 截断生效 |
| `TestWebFetchEmptyURL` | url="" → 报错 |
| `TestWebFetchInvalidURL` | url="example.com" 无 scheme → 报错 |
| `TestWebFetchPermission` | 任意 args → `Allow: true` |
| `TestWebFetchConcurrency` | `IsConcurrencySafe` / `IsReadOnly` → 均 `true` |
| `TestWebFetchResultLimit` | `ResultLimit()` → 16000 |
| `TestWebFetchStripScript` | HTML 含 `<script>` 代码 → 转换后不包含 |

## 集成

- `main.go`：在工具注册块加 `tools.Register(tool.NewWebFetchTool())`
- `go.mod` / `go.sum`：新增 `github.com/JohannesKaufmann/html-to-markdown` 依赖
- `CLAUDE.md`：在 Built-in tools 列表加 `webfetch`（HTML→Markdown，仅 GET，默认放行）
- 完成标准：`go mod tidy` + `go build ./...` + `go test ./...` 全绿

## 已知限制

- 不支持 JavaScript 渲染（无浏览器内核，如需渲染考虑 Playwright 服务）
- 不处理登录态 / Cookie（无状态请求）
- 不缓存（每次请求都重新拉取，未来可加缓存层）
- 无 robots.txt 检查（爬虫场景未来考虑）

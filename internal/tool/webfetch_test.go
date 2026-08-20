package tool

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		t.Errorf("output wrong prefix: %q", out)
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
	// html-to-markdown 不输出原始 HTML 标签
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
	// 构造循环重定向：A → B → A → B → ...
	// 用共享指针延迟绑定 URL，避免前向引用。
	var target string
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer serverB.Close()

	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverB.URL, http.StatusFound)
	}))
	defer serverA.Close()

	// 现在 serverB 反向指向 serverA，形成循环。
	target = serverA.URL

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
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}
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

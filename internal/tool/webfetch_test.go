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

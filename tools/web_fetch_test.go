package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractText(t *testing.T) {
	html := `<html><head><title>Test</title><style>body{color:red}</style></head>
<body>
<nav>Navigation stuff</nav>
<script>var x = 1;</script>
<h1>Hello World</h1>
<p>This is a <strong>test</strong> paragraph.</p>
<footer>Footer content</footer>
<aside>Sidebar</aside>
<noscript>Enable JS</noscript>
<div>Visible content here.</div>
</body></html>`

	text := extractText(html)

	if !strings.Contains(text, "Hello World") {
		t.Errorf("expected 'Hello World' in output, got: %s", text)
	}
	if !strings.Contains(text, "test paragraph") {
		t.Errorf("expected 'test paragraph' in output, got: %s", text)
	}
	if !strings.Contains(text, "Visible content") {
		t.Errorf("expected 'Visible content' in output, got: %s", text)
	}
	if strings.Contains(text, "Navigation stuff") {
		t.Errorf("expected nav content to be stripped, got: %s", text)
	}
	if strings.Contains(text, "var x = 1") {
		t.Errorf("expected script content to be stripped, got: %s", text)
	}
	if strings.Contains(text, "Footer content") {
		t.Errorf("expected footer content to be stripped, got: %s", text)
	}
	if strings.Contains(text, "Sidebar") {
		t.Errorf("expected aside content to be stripped, got: %s", text)
	}
	if strings.Contains(text, "Enable JS") {
		t.Errorf("expected noscript content to be stripped, got: %s", text)
	}
	if strings.Contains(text, "color:red") {
		t.Errorf("expected style content to be stripped, got: %s", text)
	}
}

func TestWebFetchToolHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	tool := &webFetchTool{}
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "404") {
		t.Errorf("expected 404 in error result, got: %s", result)
	}
}

func TestWebFetchToolSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><h1>Title</h1><p>Body text.</p></body></html>`))
	}))
	defer srv.Close()

	tool := &webFetchTool{}
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Title") {
		t.Errorf("expected 'Title' in result, got: %s", result)
	}
	if !strings.Contains(result, "Body text") {
		t.Errorf("expected 'Body text' in result, got: %s", result)
	}
}

func TestWebFetchToolTruncation(t *testing.T) {
	// Generate content larger than maxOutputChars.
	largeBody := "<html><body><p>" + strings.Repeat("word ", 3000) + "</p></body></html>"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(largeBody))
	}))
	defer srv.Close()

	tool := &webFetchTool{}
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(result, "[content truncated]") {
		t.Errorf("expected truncation marker, got suffix: %s", result[len(result)-50:])
	}
}

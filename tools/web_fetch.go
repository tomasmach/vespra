package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	maxFetchBytes  = 2 << 20 // 2 MB
	maxOutputChars = 8000
)

// skipTags is the set of HTML elements whose subtrees are skipped during text extraction.
var skipTags = map[string]bool{
	"script":   true,
	"style":    true,
	"noscript": true,
	"nav":      true,
	"footer":   true,
	"aside":    true,
	"svg":      true,
	"iframe":   true,
}

type webFetchTool struct {
	timeoutSeconds int
}

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch a web page and extract its readable text content. " +
		"Use when you need to read actual page content — to get current data, " +
		"read an article, or inspect a URL the user shared. " +
		"Do NOT use just to provide a link to the user."
}
func (t *webFetchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "url": {"type": "string", "description": "The URL to fetch."}
        },
        "required": ["url"]
    }`)
}

func (t *webFetchTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.URL == "" {
		return "Error: url is required", nil
	}

	timeout := t.timeoutSeconds
	if timeout <= 0 {
		timeout = 15
	}
	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, p.URL, nil)
	if err != nil {
		return fmt.Sprintf("Error: invalid URL: %s", err), nil
	}
	req.Header.Set("User-Agent", "Vespra/1.0 (Discord Bot)")
	req.Header.Set("Accept", "text/html")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Sprintf("Error: failed to fetch URL: %s", err), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf("Error: HTTP %d %s", resp.StatusCode, resp.Status), nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return fmt.Sprintf("Error: failed to read response: %s", err), nil
	}

	text := extractText(string(body))
	if len(text) > maxOutputChars {
		text = text[:maxOutputChars] + "\n\n[content truncated]"
	}
	if text == "" {
		return "No readable text content found on the page.", nil
	}
	return text, nil
}

// extractText parses HTML and returns visible text, skipping non-content elements.
func extractText(htmlContent string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(htmlContent))
	var sb strings.Builder
	var skipStack []string

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return strings.TrimSpace(collapseWhitespace(sb.String()))

		case html.StartTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)
			if skipTags[tag] {
				skipStack = append(skipStack, tag)
			}

		case html.SelfClosingTagToken:
			// Self-closing skip tags (e.g. <svg/>) open and close immediately,
			// so we do not push them onto the stack.

		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)
			if skipTags[tag] && len(skipStack) > 0 {
				// Pop the last matching entry from the stack.
				for i := len(skipStack) - 1; i >= 0; i-- {
					if skipStack[i] == tag {
						skipStack = append(skipStack[:i], skipStack[i+1:]...)
						break
					}
				}
			}
			// Insert newline after block-level elements for readability.
			if isBlockTag(tag) && len(skipStack) == 0 {
				sb.WriteByte('\n')
			}

		case html.TextToken:
			if len(skipStack) == 0 {
				text := strings.TrimSpace(tokenizer.Token().Data)
				if text != "" {
					sb.WriteString(text)
					sb.WriteByte(' ')
				}
			}
		}
	}
}

// isBlockTag reports whether the tag is a block-level element that should
// produce a line break in extracted text.
func isBlockTag(tag string) bool {
	switch tag {
	case "p", "div", "br", "h1", "h2", "h3", "h4", "h5", "h6",
		"li", "tr", "blockquote", "pre", "section", "article",
		"header", "main":
		return true
	}
	return false
}

// collapseWhitespace reduces runs of whitespace to a single space per line
// and collapses multiple blank lines into at most two newlines.
func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	blankCount := 0
	for _, line := range lines {
		trimmed := strings.Join(strings.Fields(line), " ")
		if trimmed == "" {
			blankCount++
			if blankCount <= 1 {
				result = append(result, "")
			}
			continue
		}
		blankCount = 0
		result = append(result, trimmed)
	}
	return strings.Join(result, "\n")
}

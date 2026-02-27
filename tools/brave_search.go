package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const braveSearchAPIBase = "https://api.search.brave.com/res/v1/web/search"

// BraveSearchResult represents a single search result from Brave API.
type BraveSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Desc    string `json:"description"`
	Favicon string `json:"favicon,omitempty"`
}

// BraveSearchResponse represents the full response from Brave API.
type BraveSearchResponse struct {
	Query       string              `json:"query"`
	WebResults  []BraveWebResult    `json:"web"`
}

type BraveWebResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// BraveClient handles Brave Search API requests.
type BraveClient struct {
	APIKey     string
	HTTPClient *http.Client
}

// NewBraveClient creates a new Brave search client.
func NewBraveClient(apiKey string, timeoutSeconds int) *BraveClient {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	return &BraveClient{
		APIKey: apiKey,
		HTTPClient: &http.Client{
			Timeout: time.Duration(timeoutSeconds) * time.Second,
		},
	}
}

// Search performs a web search using Brave API.
func (c *BraveClient) Search(ctx context.Context, query string, count int) ([]BraveSearchResult, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("brave API key not configured")
	}
	if count <= 0 {
		count = 10
	}
	if count > 20 {
		count = 20 // Brave max is 20
	}

	u, err := url.Parse(braveSearchAPIBase)
	if err != nil {
		return nil, fmt.Errorf("parse brave API URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", count))
	q.Set("offset", "0")
	q.Set("mkt", "en-US") // Market (can be made configurable)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave API returned %s", resp.Status)
	}

	var braveResp BraveSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&braveResp); err != nil {
		return nil, fmt.Errorf("decode brave response: %w", err)
	}

	results := make([]BraveSearchResult, 0, len(braveResp.WebResults))
	for _, r := range braveResp.WebResults {
		results = append(results, BraveSearchResult{
			Title: r.Title,
			URL:   r.URL,
			Desc:  r.Description,
		})
	}

	slog.Debug("brave search completed", "query", query, "results", len(results))
	return results, nil
}

// SearchToMarkdown performs a search and returns results as markdown text.
func (c *BraveClient) SearchToMarkdown(ctx context.Context, query string, count int) (string, error) {
	results, err := c.Search(ctx, query, count)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "No results found.", nil
	}

	var md string
	for i, r := range results {
		md += fmt.Sprintf("**%d. %s**\n\n%s\n\n%s\n\n", i+1, r.Title, r.Desc, r.URL)
	}

	return md, nil
}

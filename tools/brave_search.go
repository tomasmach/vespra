package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const braveSearchAPIBase = "https://api.search.brave.com/res/v1/web/search"

// braveSearchResult represents a single search result from Brave API.
type braveSearchResult struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Desc  string `json:"description"`
}

// braveWebBlock is the wrapper object under the "web" key in the Brave API response.
type braveWebBlock struct {
	Results []braveWebResult `json:"results"`
}

// braveWebResult is a single item within the web results block.
type braveWebResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// braveSearchResponse represents the full response from Brave API.
type braveSearchResponse struct {
	Web braveWebBlock `json:"web"`
}

// braveClient handles Brave Search API requests.
type braveClient struct {
	apiKey     string
	httpClient *http.Client
}

// newBraveClient creates a new Brave search client.
func newBraveClient(apiKey string) *braveClient {
	return &braveClient{
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

// search performs a web search using Brave API.
func (c *braveClient) search(ctx context.Context, query string, count int) ([]braveSearchResult, error) {
	if c.apiKey == "" {
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
	req.Header.Set("X-Subscription-Token", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave API returned %s", resp.Status)
	}

	var braveResp braveSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&braveResp); err != nil {
		return nil, fmt.Errorf("decode brave response: %w", err)
	}

	results := make([]braveSearchResult, 0, len(braveResp.Web.Results))
	for _, r := range braveResp.Web.Results {
		results = append(results, braveSearchResult{
			Title: r.Title,
			URL:   r.URL,
			Desc:  r.Description,
		})
	}

	slog.Debug("brave search completed", "query", query, "results", len(results))
	return results, nil
}

// searchToMarkdown performs a search and returns results as markdown text.
func (c *braveClient) searchToMarkdown(ctx context.Context, query string, count int) (string, error) {
	results, err := c.search(ctx, query, count)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "No results found.", nil
	}

	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "**%d. %s**\n\n%s\n\n%s\n\n", i+1, r.Title, r.Desc, r.URL)
	}
	return sb.String(), nil
}

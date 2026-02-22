package llm

import "time"

// SetRetryDelays overrides retryDelays for the duration of a test and returns
// a restore function to be called via t.Cleanup.
func SetRetryDelays(d []time.Duration) func() {
	orig := retryDelays
	retryDelays = d
	return func() { retryDelays = orig }
}

// SetOpenRouterBaseURL overrides the OpenRouter endpoint on a Client for testing.
// Returns a restore function to be called via t.Cleanup.
func SetOpenRouterBaseURL(c *Client, url string) func() {
	orig := c.openRouterBaseURL
	c.openRouterBaseURL = url
	return func() { c.openRouterBaseURL = orig }
}

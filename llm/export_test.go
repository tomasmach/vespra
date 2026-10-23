package llm

import "time"

// SetRetryDelays overrides retryDelays for the duration of a test and returns
// a restore function to be called via t.Cleanup.
func SetRetryDelays(d []time.Duration) func() {
	orig := retryDelays
	retryDelays = d
	return func() { retryDelays = orig }
}

// SetExplabsBaseURL overrides the gateway endpoint on a Client for testing.
// Returns a restore function to be called via t.Cleanup.
func SetExplabsBaseURL(c *Client, url string) func() {
	orig := c.explabsBaseURL
	c.explabsBaseURL = url
	return func() { c.explabsBaseURL = orig }
}

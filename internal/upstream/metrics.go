package upstream

import "net/http"

func (c *Client) recordAttempt(path string, response *http.Response) {
	if c.metrics == nil {
		return
	}
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	c.metrics.Upstream(path, status)
}

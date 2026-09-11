package obs

import "strconv"

func (m *Metrics) Upstream(endpoint string, status int) {
	switch endpoint {
	case "models", "model/info", "chat/completions", "completions", "embeddings", "responses", "responses/compact", "messages", "audio/transcriptions":
	default:
		endpoint = "other"
	}
	code := "error"
	if status >= 100 && status <= 599 {
		code = strconv.Itoa(status)
	}
	m.upstreamRequests.WithLabelValues(endpoint, code).Inc()
}

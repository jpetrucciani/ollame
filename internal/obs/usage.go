package obs

import "strconv"

type TokenUsage struct {
	Prompt, Completion, Cached *int
	Estimated                  bool
}

// Callers supply the name of the resolved entry in their captured catalog.
// Unknown counts stay absent; explicit zeros remain observable.
func (m *Metrics) Usage(model, token string, usage TokenUsage) {
	if model == "" {
		return
	}
	for _, part := range []struct {
		kind  string
		count *int
	}{{"prompt", usage.Prompt}, {"completion", usage.Completion}, {"cached", usage.Cached}} {
		if part.count == nil || *part.count < 0 {
			continue
		}
		estimated := strconv.FormatBool(usage.Estimated)
		m.tokensByToken.WithLabelValues(model, token, part.kind, estimated).Add(float64(*part.count))
		m.tokensAggregate.WithLabelValues(model, part.kind, estimated).Add(float64(*part.count))
	}
}
func (m *Metrics) TTFT(model string, seconds float64) {
	if model != "" && seconds >= 0 {
		m.ttft.WithLabelValues(model).Observe(seconds)
	}
}

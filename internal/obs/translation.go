package obs

// Drop receives a normalized name from translate.MetricField, never client text.
func (m *Metrics) Drop(field string) { m.dropped.WithLabelValues(field).Inc() }
func (m *Metrics) Synthesized(count int) {
	if count > 0 {
		m.synthesized.Add(float64(count))
	}
}
func (m *Metrics) ContentFiltered(model string) {
	if model != "" {
		m.filtered.WithLabelValues(model).Inc()
	}
}

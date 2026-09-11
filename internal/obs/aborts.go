package obs

type AbortReason string

const (
	AbortShutdown AbortReason = "shutdown"
	AbortClient   AbortReason = "client_disconnect"
	AbortIdle     AbortReason = "idle_timeout"
	AbortUpstream AbortReason = "upstream_error"
)

func (m *Metrics) Abort(reason AbortReason) { m.aborts.WithLabelValues(string(reason)).Inc() }

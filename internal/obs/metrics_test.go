package obs

import (
	"testing"
	"time"
)

func TestOperationalMetrics(t *testing.T) {
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	m.Reload(ReloadUpstreamFailed)
	m.SourceError("file", true)
	m.SourceError("/private/untrusted/path", false)
	m.CatalogError()
	m.CatalogSuccess(time.Unix(123, 0), 2, 1)
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, family := range families {
		found[family.GetName()] = true
		for _, metric := range family.Metric {
			switch family.GetName() {
			case "ollame_catalog_last_success_timestamp_seconds":
				if metric.GetGauge().GetValue() != 123 {
					t.Fatal("wrong success timestamp")
				}
			case "ollame_catalog_refresh_errors_total":
				if metric.GetCounter().GetValue() != 1 {
					t.Fatal("wrong refresh failure count")
				}
			case "ollame_config_reloads_total":
				if metric.GetCounter().GetValue() != 1 || metric.Label[0].GetValue() != "upstream_failed" {
					t.Fatal("wrong reload outcome")
				}
			}
			for _, label := range metric.Label {
				if label.GetValue() == "/private/untrusted/path" {
					t.Fatal("unbounded source label escaped")
				}
			}
		}
	}
	for _, name := range []string{"ollame_config_reloads_total", "ollame_secret_source_errors_total", "ollame_catalog_refresh_errors_total", "ollame_catalog_last_success_timestamp_seconds", "ollame_catalog_models"} {
		if !found[name] {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestRequestMetricViews(t *testing.T) {
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	m.Request("/api/chat", "first", 200, 250*time.Millisecond)
	m.Request("/api/chat", "second", 200, time.Second)
	m.AuthFailure(true)
	m.AuthFailure(false)
	labeled, err := m.labeledRegistry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := m.aggregateRegistry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(labeled) != 1 || len(labeled[0].Metric) != 2 {
		t.Fatal("token series not distinct")
	}
	if len(aggregate) != 1 || len(aggregate[0].Metric) != 1 || aggregate[0].Metric[0].GetCounter().GetValue() != 2 {
		t.Fatal("aggregate counts not preserved")
	}
	for _, label := range aggregate[0].Metric[0].Label {
		if label.GetName() == "token" {
			t.Fatal("disabled token label still exported")
		}
	}
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundDuration, foundAuth := false, false
	for _, family := range families {
		switch family.GetName() {
		case "ollame_request_duration_seconds":
			histogram := family.Metric[0].GetHistogram()
			if histogram.GetSampleCount() != 2 || histogram.GetSampleSum() != 1.25 {
				t.Fatal("duration observations incorrect")
			}
			foundDuration = true
		case "ollame_auth_failures_total":
			if len(family.Metric) != 2 {
				t.Fatal("auth reasons not separated")
			}
			foundAuth = true
		}
	}
	if !foundDuration || !foundAuth {
		t.Fatal("request collectors missing")
	}
}

func TestUsageMetrics(t *testing.T) {
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	prompt, completion, zero, negative := 10, 3, 0, -1
	m.Usage("listed:latest", "ci", TokenUsage{Prompt: &prompt, Completion: &completion, Cached: &zero})
	m.Usage("listed:latest", "ci", TokenUsage{Prompt: &prompt})
	m.Usage("", "ci", TokenUsage{Prompt: &prompt})
	m.Usage("listed:latest", "ci", TokenUsage{Completion: &negative})
	m.TTFT("listed:latest", .25)
	families, err := m.aggregateRegistry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "ollame_tokens_total" {
			continue
		}
		for _, metric := range family.Metric {
			kind := ""
			for _, label := range metric.Label {
				switch label.GetName() {
				case "token":
					t.Fatal("aggregate usage exposed token attribution")
				case "model":
					if label.GetValue() != "listed:latest" {
						t.Fatal("unresolved model created usage")
					}
				case "estimated":
					if label.GetValue() != "false" {
						t.Fatal("observed tokens marked estimated")
					}
				case "kind":
					kind = label.GetValue()
				}
			}
			values[kind] = metric.GetCounter().GetValue()
		}
	}
	if len(values) != 3 || values["prompt"] != 20 || values["completion"] != 3 || values["cached"] != 0 {
		t.Fatalf("wrong observed usage: %v", values)
	}
	families, err = m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() == "ollame_upstream_ttft_seconds" {
			found = true
			histogram := family.Metric[0].GetHistogram()
			if histogram.GetSampleCount() != 1 || histogram.GetSampleSum() != .25 {
				t.Fatal("incorrect TTFT")
			}
		}
	}
	if !found {
		t.Fatal("TTFT observation missing")
	}
}

func TestTranslationMetrics(t *testing.T) {
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	m.Drop("num_ctx")
	m.Drop("other")
	m.Synthesized(2)
	m.Synthesized(-1)
	m.ContentFiltered("listed:latest")
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	dropped, synthesized, filtered := false, false, false
	for _, family := range families {
		switch family.GetName() {
		case "ollame_translation_dropped_total":
			if len(family.Metric) != 2 {
				t.Fatal("drop series incorrect")
			}
			for _, metric := range family.Metric {
				if metric.GetCounter().GetValue() != 1 {
					t.Fatal("drop count incorrect")
				}
			}
			dropped = true
		case "ollame_tool_results_synthesized_total":
			if family.Metric[0].GetCounter().GetValue() != 2 {
				t.Fatal("synthesis count incorrect")
			}
			synthesized = true
		case "ollame_content_filtered_total":
			if family.Metric[0].GetCounter().GetValue() != 1 || family.Metric[0].Label[0].GetValue() != "listed:latest" {
				t.Fatal("filtered response attribution incorrect")
			}
			filtered = true
		}
	}
	if !dropped || !synthesized || !filtered {
		t.Fatal("translation metric missing")
	}
}

func TestUpstreamAttemptMetrics(t *testing.T) {
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	m.Upstream("chat/completions", 503)
	m.Upstream("chat/completions", 503)
	m.Upstream("chat/completions", 200)
	m.Upstream("models", 0)
	m.Upstream("https://secret@example.invalid/private", 99999)
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() != "ollame_upstream_requests_total" {
			continue
		}
		found = true
		if len(family.Metric) != 4 {
			t.Fatal("unexpected attempt series")
		}
		for _, metric := range family.Metric {
			endpoint, code := "", ""
			for _, label := range metric.Label {
				switch label.GetName() {
				case "endpoint":
					endpoint = label.GetValue()
				case "code":
					code = label.GetValue()
				}
			}
			want := float64(1)
			if endpoint == "chat/completions" && code == "503" {
				want = 2
			}
			if metric.GetCounter().GetValue() != want {
				t.Fatal("attempts were collapsed")
			}
			if endpoint != "chat/completions" && endpoint != "models" && endpoint != "other" {
				t.Fatal("unbounded endpoint label")
			}
			if endpoint == "other" && code != "error" {
				t.Fatal("unbounded status label")
			}
		}
	}
	if !found {
		t.Fatal("upstream attempt counter missing")
	}
}

func TestAbortMetrics(t *testing.T) {
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []AbortReason{AbortShutdown, AbortClient, AbortIdle, AbortUpstream} {
		m.Abort(reason)
	}
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "ollame_stream_aborts_total" {
			if len(family.Metric) != 4 {
				t.Fatal("wrong abort reason count")
			}
			for _, metric := range family.Metric {
				if metric.GetCounter().GetValue() != 1 {
					t.Fatal("wrong abort counter")
				}
			}
			return
		}
	}
	t.Fatal("abort collector missing")
}

func TestMixedEmbeddingUsageMetrics(t *testing.T) {
	m, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	reported, estimated := 7, 2
	m.Usage("listed:latest", "ci", TokenUsage{Prompt: &reported})
	m.Usage("listed:latest", "ci", TokenUsage{Prompt: &estimated, Estimated: true})
	families, err := m.aggregateRegistry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "ollame_tokens_total" {
			continue
		}
		for _, metric := range family.Metric {
			provenance := ""
			for _, label := range metric.Label {
				if label.GetName() == "estimated" {
					provenance = label.GetValue()
				}
				if label.GetName() == "kind" && label.GetValue() != "prompt" {
					t.Fatal("invented completion or cache counts")
				}
			}
			values[provenance] = metric.GetCounter().GetValue()
		}
	}
	if len(values) != 2 || values["false"] != 7 || values["true"] != 2 {
		t.Fatalf("mixed usage mislabeled: %v", values)
	}
}

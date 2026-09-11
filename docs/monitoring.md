# Monitoring

Scrape `/metrics` on the separate admin listener (default port 9434).
`metrics.enabled` must be true. The API listener does not expose metrics or debug
routes. Restrict admin access to your monitoring network; authentication on the
API listener does not protect the admin listener. Kubernetes policy and probe
behavior need validation against the deployment's CNI.

Load `deploy/monitoring/alerts.yaml` with Prometheus `rule_files`. For example,
when the file is mounted at `/etc/prometheus/ollame-alerts.yaml`:

```yaml
rule_files:
  - /etc/prometheus/ollame-alerts.yaml
scrape_configs:
  - job_name: ollame
    scrape_interval: 30s
    static_configs:
      - targets: [ollame-admin:9434]
```

Replace the target with addresses that identify individual replicas. The rules
preserve scrape labels, including `job` and `instance`, so a healthy replica does
not hide another replica's stale catalog or secret failures. These are plain
Prometheus rules; wrap the groups in a PrometheusRule resource if your deployment
uses the Prometheus Operator. Alertmanager routing remains deployment-owned.

| Alert                        | Default condition                                                                       | Response                                                                                          |
| ---------------------------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| OllameCatalogStale           | No successful catalog, or last success older than 5 minutes, continuously for 2 minutes | Check discovery connectivity, credentials and catalog errors.                                     |
| OllameSecretSourceFailures   | At least 3 read/parse errors in 5 minutes, continuously for 1 minute                    | Restore source contents or permissions before token stale grace expires.                          |
| OllameTokenSourceExpired     | A stale-expiry counter increase in the last 5 minutes                                   | Tokens from that source were dropped. Restore a valid readable source.                            |
| OllameUpstreamReloadFailures | At least 3 rejected upstream candidates in 5 minutes, continuously for 1 minute         | Verify the candidate key/URL can discover its catalog. The old upstream snapshot is still active. |

The catalog threshold assumes the default 60-second refresh interval. Tune the
threshold and firing delays when changing refresh/reload intervals or token stale
grace. Counter-based rules need more than one scrape, so a newly created series
cannot reliably identify its first event. The expiry alert can remain active for
five minutes after recovery while the increase is still in its lookback window.

A catalog refresh failure intentionally does not remove a previously ready
replica from service. Readiness is therefore not a substitute for these alerts.
Use your normal target-down and missing-scrape alerts too: rules based on ollame
metrics cannot diagnose a target whose metrics have disappeared entirely.

`metrics.token_labels=false` removes token attribution from exported usage and
request metrics. Model labels come from the exposed catalog. Dropped-field labels
use a fixed vocabulary, with unknown fields grouped as `other`. Estimated usage
has `estimated="true"`; mixed embedding batches keep separate reported and
estimated series. Do not combine those series when reporting measured spend.

`admin.debug=true` enables `/debug/config` and `/debug/catalog` on the admin
listener. Config output includes redacted effective values, provenance, and the
snapshot generation. Profiling additionally requires `log.level="debug"`.
Keep debug disabled during normal operation.

Validate syntax and alert firing/recovery with the pinned Prometheus 3.5.0
`promtool` image (no network access inside the test container):

```sh
OLLAME_TEST_MONITORING=1 CGO_ENABLED=0 go test ./test -run '^TestPrometheusAlertRules$' -count=1
```

The test covers pending/firing transitions, never-initialized and healthy
catalogs, recovery, isolated errors, stale expiry, and counter resets. It validates
PromQL behavior, not a deployed scrape, Alertmanager notification, or NetworkPolicy.
The official [Prometheus rule-test documentation](https://prometheus.io/docs/prometheus/latest/configuration/unit_testing_rules/)
describes the input-series format used in the test corpus.

The admin listener defaults to `127.0.0.1:9434`. For scraping across a container
network, explicitly set `OLLAME_SERVER_ADMIN_LISTEN=:9434` and restrict network
access to monitoring clients. Admin endpoints do not use client authentication.

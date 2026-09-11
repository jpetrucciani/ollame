package test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPrometheusAlertRules(t *testing.T) {
	if os.Getenv("OLLAME_TEST_MONITORING") != "1" {
		t.Skip("set OLLAME_TEST_MONITORING=1 with Docker available")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range [][]string{{"check", "rules", "deploy/monitoring/alerts.yaml"}, {"test", "rules", "test/monitoring/alerts.test.yaml"}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		args := []string{"run", "--rm", "--network", "none", "-v", root + ":/work:ro", "-w", "/work", "--entrypoint", "/bin/promtool", "prom/prometheus@sha256:63805ebb8d2b3920190daf1cb14a60871b16fd38bed42b857a3182bc621f4996"}
		output, err := exec.CommandContext(ctx, "docker", append(args, check...)...).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("promtool %v: %v\n%s", check, err, output)
		}
	}
}

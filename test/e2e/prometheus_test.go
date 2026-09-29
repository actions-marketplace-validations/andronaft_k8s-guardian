//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCostWithRealPrometheus backfills a week of synthetic cAdvisor metrics
// into a real Prometheus and checks `cost --prometheus` reads p95 CPU and
// peak memory for the right pods only.
func TestCostWithRealPrometheus(t *testing.T) {
	for _, bin := range []string{"prometheus", "promtool"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH", bin)
		}
	}
	dir := t.TempDir()
	now := time.Now().Unix() / 60 * 60
	start := now - 3*86400
	const mi = 1 << 20
	series := []struct {
		pod         string
		base, spike float64
		mem, peak   int
	}{
		{"orders-api-7d9f8c6b5-x2k4p", 0.020, 0.250, 50 * mi, 140 * mi},
		{"orders-api-7d9f8c6b5-m7q9z", 0.030, 0.200, 60 * mi, 100 * mi},
		{"orders-api-worker-5f7b9c8d4-abcde", 3, 3, 3072 * mi, 3072 * mi}, // decoy, different Deployment
	}
	var b strings.Builder
	b.WriteString("# TYPE container_cpu_usage_seconds_total counter\n")
	for _, s := range series {
		total := 0.0
		for ts := start; ts <= now; ts += 60 {
			rate := s.base
			if (ts-start)%21600 < 4320 { // spikes 20% of the time -> p95 is the spike
				rate = s.spike
			}
			total += rate * 60
			fmt.Fprintf(&b, "container_cpu_usage_seconds_total{namespace=\"shop\",pod=%q,container=\"api\"} %.3f %d\n", s.pod, total, ts)
		}
	}
	b.WriteString("# TYPE container_memory_working_set_bytes gauge\n")
	for _, s := range series {
		for ts := start; ts <= now; ts += 60 {
			v := s.mem
			if ts == start+86400 {
				v = s.peak
			}
			fmt.Fprintf(&b, "container_memory_working_set_bytes{namespace=\"shop\",pod=%q,container=\"api\"} %d %d\n", s.pod, v, ts)
		}
	}
	b.WriteString("# EOF\n")
	om, data := filepath.Join(dir, "metrics.om"), filepath.Join(dir, "data")
	os.WriteFile(om, []byte(b.String()), 0o600)
	if out, err := exec.Command("promtool", "tsdb", "create-blocks-from", "openmetrics", om, data).CombinedOutput(); err != nil {
		t.Fatalf("promtool: %v\n%s", err, out)
	}
	cfg := filepath.Join(dir, "prometheus.yml")
	os.WriteFile(cfg, []byte("global: {}\n"), 0o600)
	addr := fmt.Sprintf("127.0.0.1:%d", freePort())
	prom := exec.Command("prometheus", "--config.file="+cfg, "--storage.tsdb.path="+data,
		"--storage.tsdb.retention.time=30d", "--web.listen-address="+addr)
	if err := prom.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { prom.Process.Kill(); prom.Wait() }()
	url := "http://" + addr
	for i := 0; i < 60; i++ {
		if resp, err := http.Get(url + "/-/ready"); err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	code, out := run("cost", "-f", "../../examples/costly-app.yaml", "--prometheus", url, "--window", "3d")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"usage from Prometheus over 3d: p95 CPU and peak memory of 2 pod(s)",
		`"api" uses cpu 250m / memory 140Mi (requests 4 / 8Gi): suggest cpu 315m, memory 176Mi`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

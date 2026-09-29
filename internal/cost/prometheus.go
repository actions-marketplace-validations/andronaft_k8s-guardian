package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/quantity"
)

// Prometheus reads usage history (cAdvisor metrics) from Prometheus: p95 of
// CPU and the peak of the memory working set over Window. Because peaks are
// already in the data, the headroom is smaller than for snapshots.
type Prometheus struct {
	URL    string
	Window string // PromQL duration, e.g. 7d
	Token  string // optional bearer token (K8S_GUARDIAN_PROMETHEUS_TOKEN)
	Client *http.Client
}

// NewPrometheus validates the URL and window.
func NewPrometheus(rawURL, window string) (*Prometheus, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid Prometheus URL %q", rawURL)
	}
	if !regexp.MustCompile(`^[0-9]+[smhdwy]$`).MatchString(window) {
		return nil, fmt.Errorf("invalid window %q (use e.g. 24h, 7d, 2w)", window)
	}
	return &Prometheus{URL: strings.TrimRight(rawURL, "/"), Window: window,
		Token: os.Getenv("K8S_GUARDIAN_PROMETHEUS_TOKEN"), Client: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Pod names are <owner>-<suffix> where generated suffixes use Kubernetes'
// "safe" alphabet (no vowels), so e.g. "orders-api-worker-…" never matches
// the pods of Deployment "orders-api".
const safe = `[bcdfghjklmnpqrstvwxz2456789]`

// PodRegex returns the regex matching the pods of a workload.
func PodRegex(kind, name string) string {
	n := regexp.QuoteMeta(name)
	switch kind {
	case "Deployment":
		return n + "-" + safe + "{1,10}-" + safe + "{5}"
	case "StatefulSet":
		return n + "-[0-9]+"
	case "DaemonSet", "ReplicaSet", "ReplicationController":
		return n + "-" + safe + "{5}"
	}
	return n
}

// Queries returns the PromQL for CPU (p95 cores), memory (peak bytes) and pod count.
func (p *Prometheus) Queries(ns, podRegex string) (cpu, mem, pods string) {
	sel := fmt.Sprintf(`namespace=%s,pod=~%s,container!="",container!="POD"`, strconv.Quote(ns), strconv.Quote(podRegex))
	cpu = fmt.Sprintf(`max by (container) (quantile_over_time(0.95, rate(container_cpu_usage_seconds_total{%s}[5m])[%s:5m]))`, sel, p.Window)
	mem = fmt.Sprintf(`max by (container) (max_over_time(container_memory_working_set_bytes{%s}[%s]))`, sel, p.Window)
	pods = fmt.Sprintf(`count(count by (pod) (max_over_time(container_memory_working_set_bytes{%s}[%s])))`, sel, p.Window)
	return
}

func (p *Prometheus) Usage(w *Workload, ns string) (map[string]cluster.Usage, int, error) {
	name := w.target.Obj.Name()
	cpuQ, memQ, podsQ := p.Queries(ns, PodRegex(w.Kind, name))
	cpu, err := p.query(cpuQ)
	if err != nil {
		return nil, 0, err
	}
	mem, err := p.query(memQ)
	if err != nil {
		return nil, 0, err
	}
	count, err := p.query(podsQ)
	if err != nil {
		return nil, 0, err
	}
	out := map[string]cluster.Usage{}
	for c, v := range mem {
		out[c] = cluster.Usage{
			CPU:    quantity.FormatCPU(int64(math.Round(cpu[c] * 1000))),
			Memory: quantity.FormatBytes(int64(math.Round(v))),
		}
	}
	return out, int(count[""]), nil
}

func (p *Prometheus) Headroom() (float64, float64) { return 1.25, 1.2 }

func (p *Prometheus) Describe(pods int) string {
	return fmt.Sprintf("usage from Prometheus over %s: p95 CPU and peak memory of %d pod(s)", p.Window, pods)
}

// query runs an instant query and returns value by "container" label.
func (p *Prometheus) query(q string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", p.URL+"/api/v1/query?query="+url.QueryEscape(q), nil)
	if err != nil {
		return nil, err
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("prometheus: HTTP %d: %.200s", resp.StatusCode, body)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", r.Error)
	}
	out := map[string]float64{}
	for _, s := range r.Data.Result {
		str, _ := s.Value[1].(string)
		v, err := strconv.ParseFloat(str, 64)
		if err != nil || math.IsNaN(v) {
			continue
		}
		out[s.Metric["container"]] = v
	}
	return out, nil
}

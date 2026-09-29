package cost

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestPodRegex(t *testing.T) {
	cases := []struct {
		kind, name, pod string
		match           bool
	}{
		{"Deployment", "orders-api", "orders-api-7d9f8c6b5-x2k4p", true},
		{"Deployment", "orders-api", "orders-api-worker-5f7b9c8d4-bcdfg", false}, // vowels: not a hash
		{"Deployment", "orders-api", "orders-api-7d9f8c6b5", false},
		{"StatefulSet", "db", "db-0", true},
		{"StatefulSet", "db", "db-main-0", false},
		{"DaemonSet", "agent", "agent-x2k4p", true},
		{"Deployment", "a.b", "axb-7d9f8c6b5-x2k4p", false}, // dots are quoted
	}
	for _, c := range cases {
		re := regexp.MustCompile("^" + PodRegex(c.kind, c.name) + "$")
		if re.MatchString(c.pod) != c.match {
			t.Errorf("%s %s vs %s: want %v", c.kind, c.name, c.pod, c.match)
		}
	}
}

func TestPrometheusSource(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing bearer token")
		}
		switch {
		case strings.HasPrefix(q, "count("):
			w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{},"value":[0,"3"]}]}}`))
		case strings.Contains(q, "cpu"):
			w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"container":"api"},"value":[0,"0.0425"]}]}}`))
		default:
			w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"container":"api"},"value":[0,"104857600"]}]}}`))
		}
	}))
	defer srv.Close()
	t.Setenv("K8S_GUARDIAN_PROMETHEUS_TOKEN", "secret")
	p, err := NewPrometheus(srv.URL, "7d")
	if err != nil {
		t.Fatal(err)
	}
	ws := estimate(t, `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: prod}
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: api
          image: x:1
          resources: {requests: {cpu: "1", memory: 1Gi}}
`)
	notes := AttachUsage(ws, p, "default", Pricing{CPUHour: 0.03, GiBHour: 0.004})
	if len(notes) != 0 {
		t.Fatalf("notes: %v", notes)
	}
	c := ws[0].Containers[0]
	if c.UsedCPU != 43 || c.UsedMemory != 100<<20 || c.SuggestedCPU != 55 || c.SuggestedMemory != 128<<20 {
		t.Errorf("unexpected usage %+v", c)
	}
	if !strings.Contains(queries[0], `namespace="prod",pod=~"web-`) || !strings.Contains(queries[0], "[7d:5m]") {
		t.Errorf("unexpected query %s", queries[0])
	}
	if !strings.Contains(ws[0].Notes[len(ws[0].Notes)-1], "over 7d: p95 CPU and peak memory of 3 pod(s)") {
		t.Errorf("notes: %v", ws[0].Notes)
	}
	if _, err := NewPrometheus("ftp://x", "7d"); err == nil {
		t.Error("expected invalid URL error")
	}
	if _, err := NewPrometheus(srv.URL, "7 days"); err == nil {
		t.Error("expected invalid window error")
	}
}

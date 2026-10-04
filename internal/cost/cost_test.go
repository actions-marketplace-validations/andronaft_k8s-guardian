package cost

import (
	"math"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/ai"
	"github.com/andronaft/k8s-guardian/internal/manifest"
)

func TestEstimateAndApply(t *testing.T) {
	data, _ := os.ReadFile("../../examples/costly-app.yaml")
	f, _ := manifest.Parse(data, "costly.yaml")
	p := Pricing{CPUHour: 0.03, GiBHour: 0.004}
	ws := Estimate(f.Objects, p)
	if len(ws) != 1 {
		t.Fatalf("expected 1 workload, got %d", len(ws))
	}
	w := ws[0]
	if w.MinReplicas != 4 || w.MaxReplicas != 10 {
		t.Errorf("HPA range not applied: %d-%d", w.MinReplicas, w.MaxReplicas)
	}
	pod := (4*0.03 + 8*0.004) * HoursPerMonth
	if math.Abs(w.MonthlyMin-4*pod) > 0.01 || math.Abs(w.MonthlyMax-10*pod) > 0.01 {
		t.Errorf("unexpected monthly cost %.2f-%.2f", w.MonthlyMin, w.MonthlyMax)
	}

	adv := []Advice{{Recommendation: ai.Recommendation{Resource: "shop/Deployment/orders-api", Container: "api", CPURequest: "500m", MemoryRequest: "512Mi", MemoryLimit: "1Gi"}}}
	if n := Apply(ws, adv); n != 1 {
		t.Fatalf("expected 1 applied recommendation")
	}
	out, _ := f.Encode()
	for _, want := range []string{"cpu: 500m", "memory: 512Mi", "memory: 1Gi", "# Go HTTP service"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func estimate(t *testing.T, doc string) []*Workload {
	t.Helper()
	f, err := manifest.Parse([]byte(doc), "w.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return Estimate(f.Objects, Pricing{CPUHour: 0.03, GiBHour: 0.004})
}

func TestRolloutCost(t *testing.T) {
	ws := estimate(t, `apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata: {name: web, namespace: shop}
spec:
  replicas: 3
  template:
    spec:
      containers: [{name: web, image: nginx:1.27, resources: {requests: {cpu: "1", memory: 1Gi}}}]
`)
	if len(ws) != 1 || ws[0].MinReplicas != 3 {
		t.Fatalf("unexpected estimate: %+v", ws)
	}
	if got := PodRegex("Rollout", "web"); !regexp.MustCompile("^" + got + "$").MatchString("web-7c5ddbdf54-x2b4k") {
		t.Errorf("rollout pod regex %q does not match", got)
	}
}

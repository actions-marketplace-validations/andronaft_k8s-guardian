package cluster

import "testing"

func TestParseTop(t *testing.T) {
	u, pods, err := ParseTop("a-1  app  10m  100Mi\na-1  sidecar  2m  20Mi\na-2  app  31m  200Mi\ngarbage\n")
	if err != nil || pods != 2 {
		t.Fatalf("pods=%d err=%v", pods, err)
	}
	if u["app"].CPU != "21m" || u["app"].Memory != "150Mi" || u["sidecar"].CPU != "2m" {
		t.Errorf("unexpected usage %+v", u)
	}
}

func TestResourceArg(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"Deployment", "apps/v1"}:                      "deployment.apps",
		{"Service", "v1"}:                              "service",
		{"ServiceMonitor", "monitoring.coreos.com/v1"}: "servicemonitor.monitoring.coreos.com",
	} {
		if got := resourceArg(in[0], in[1]); got != want {
			t.Errorf("resourceArg(%v) = %q, want %q", in, got, want)
		}
	}
}

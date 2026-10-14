package cluster

import (
	"errors"
	"strings"
	"testing"
)

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

func TestDryRunError(t *testing.T) {
	exit := errors.New("exit status 1")
	for _, tc := range []struct {
		stderr string
		want   string // "rejection: <message>", "forbidden", "notfound" or "error"
	}{
		// Messages as kubectl 1.37 prints them for a 1.37 API server.
		{`The Deployment "orders-api" is invalid: spec.selector: Invalid value: {"matchLabels":{"app":"orders-api"}}: field is immutable`,
			`rejection: spec.selector: Invalid value: {"matchLabels":{"app":"orders-api"}}: field is immutable`},
		{`The deployments "web" is invalid: : ValidatingAdmissionPolicy 'no-latest' with binding 'no-latest' denied request: images must not use :latest`,
			`rejection: ValidatingAdmissionPolicy 'no-latest' with binding 'no-latest' denied request: images must not use :latest`},
		{`Error from server: failed to create typed patch object (shop/orders-api; apps/v1, Kind=Deployment): .spec.foo: field not declared in schema`,
			`rejection: .spec.foo: field not declared in schema`},
		{`Error from server (Forbidden): pods "web" is forbidden: exceeded quota: compute, requested: requests.cpu=4, used: requests.cpu=8, limited: requests.cpu=10`,
			`rejection: exceeded quota: compute, requested: requests.cpu=4, used: requests.cpu=8, limited: requests.cpu=10`},
		{"Warning: would violate PodSecurity \"restricted:latest\": runAsNonRoot != true\nError from server (Forbidden): admission webhook \"validate.kyverno.svc-fail\" denied the request: require-labels",
			`rejection: admission webhook "validate.kyverno.svc-fail" denied the request: require-labels`},
		{`Error from server (Forbidden): deployments.apps "orders-api" is forbidden: User "alice" cannot patch resource "deployments" in API group "apps" in the namespace "shop"`,
			"forbidden"},
		{`Error from server (NotFound): namespaces "nope" not found`, "notfound"},
		{`Error from server (BadRequest): admission webhook "sidecar.example.com" does not support dry run`, "error"},
		{"error: resource mapping not found for name: \"w\" namespace: \"shop\" from \"STDIN\": no matches for kind \"Widget\" in version \"example.com/v1\"\nensure CRDs are installed first", "error"},
		{`Error from server (InternalError): Internal error occurred: failed calling webhook "x.example.com": connection refused`, "error"},
		{"", "error"},
	} {
		err := dryRunError(tc.stderr, exit)
		var rej *Rejection
		got := "error"
		switch {
		case errors.As(err, &rej):
			got = "rejection: " + rej.Message
		case errors.Is(err, ErrForbidden):
			got = "forbidden"
		case errors.Is(err, ErrNotFound):
			got = "notfound"
		}
		if got != tc.want {
			t.Errorf("%s\n got: %s\nwant: %s", tc.stderr, got, tc.want)
		}
	}
}

func TestWarnings(t *testing.T) {
	got := warnings("Warning: would violate PodSecurity \"restricted:latest\": runAsNonRoot != true\nWarning: apps/v1beta1 is deprecated\n\nsomething else\nWarning: \n")
	want := []string{`would violate PodSecurity "restricted:latest": runAsNonRoot != true`, "apps/v1beta1 is deprecated"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("warnings = %q, want %q", got, want)
	}
}

func TestParseNodeRequests(t *testing.T) {
	got := ParseNodeRequests(strings.Join([]string{
		"n1|500m,1Gi;250m,256Mi;||",                 // two containers
		"n1|100m,64Mi;|2,1Gi,;|10m,32Mi",            // an init container bigger than the app, plus overhead
		"n2|1,1Gi;|200m,128Mi,Always;300m,512Mi,;|", // a sidecar adds to the app and to later init containers
		"|1,1Gi;||", // not scheduled yet
		"garbage",
	}, "\n"))
	want := map[string]Requests{
		"n1": {MilliCPU: 750 + 2000 + 10, Bytes: (1024+256)<<20 + 1<<30 + 32<<20},
		"n2": {MilliCPU: 1200, Bytes: 1<<30 + 128<<20},
	}
	if len(got) != len(want) || got["n1"] != want["n1"] || got["n2"] != want["n2"] {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

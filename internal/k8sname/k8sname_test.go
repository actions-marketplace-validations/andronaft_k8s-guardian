package k8sname

import "testing"

func TestRejectsFlagInjection(t *testing.T) {
	bad := []string{"--server=https://attacker", "-n", "a b", "ns;rm", "", "UPPER", "x/../y", "a\nb"}
	for _, v := range bad {
		if ValidNamespace(v) == nil {
			t.Errorf("namespace %q accepted", v)
		}
	}
	for _, v := range []string{"--kubeconfig=/x", "-o", "a b", "a/b"} {
		if ValidName(v) == nil {
			t.Errorf("name %q accepted", v)
		}
	}
	for _, v := range []string{"--server=x", "deployments --raw", "-A", "deploy/x;y"} {
		if ValidResourceArg(v) == nil {
			t.Errorf("resource %q accepted", v)
		}
	}
	if ValidSelector(map[string]string{"app": "x,evil=1"}) == nil || ValidSelector(map[string]string{"--server": "x"}) == nil {
		t.Error("bad selector accepted")
	}
	for _, v := range []string{"default", "kube-system", "a1"} {
		if err := ValidNamespace(v); err != nil {
			t.Error(err)
		}
	}
	for _, v := range []string{"web", "system:aggregate-to-admin", "my.app-1"} {
		if err := ValidName(v); err != nil {
			t.Error(err)
		}
	}
	for _, v := range []string{"deployment/web", "deployments,statefulsets", "deployment.apps/web"} {
		if err := ValidResourceArg(v); err != nil {
			t.Error(err)
		}
	}
	if err := ValidSelector(map[string]string{"app.kubernetes.io/name": "web", "tier": ""}); err != nil {
		t.Error(err)
	}
}

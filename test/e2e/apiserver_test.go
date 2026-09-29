//go:build e2e

// Package e2e runs k8s-guardian against a real kube-apiserver + etcd
// (envtest binaries, no nodes): live checks, diff and exported
// ValidatingAdmissionPolicies. Run with
//
//	KUBEBUILDER_ASSETS=$(setup-envtest use -p path) go test -tags e2e ./test/e2e/
package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var kubeconfig string

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestMain(m *testing.M) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		fmt.Println("KUBEBUILDER_ASSETS not set: skipping e2e tests")
		os.Exit(0)
	}
	dir, _ := os.MkdirTemp("", "k8s-guardian-e2e")
	code := 1
	stop, err := startAPIServer(assets, dir)
	if err != nil {
		fmt.Println("starting kube-apiserver:", err)
	} else {
		code = m.Run()
		stop()
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

func startAPIServer(assets, dir string) (func(), error) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	os.WriteFile(filepath.Join(dir, "sa.key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	os.WriteFile(filepath.Join(dir, "sa.pub"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0o600)
	os.WriteFile(filepath.Join(dir, "tokens.csv"), []byte("e2etoken,admin,admin,system:masters\n"), 0o600)

	etcdClient, etcdPeer, apiPort := freePort(), freePort(), freePort()
	etcd := exec.Command(filepath.Join(assets, "etcd"), "--data-dir", filepath.Join(dir, "etcd"),
		"--listen-client-urls", fmt.Sprintf("http://127.0.0.1:%d", etcdClient),
		"--advertise-client-urls", fmt.Sprintf("http://127.0.0.1:%d", etcdClient),
		"--listen-peer-urls", fmt.Sprintf("http://127.0.0.1:%d", etcdPeer),
		"--initial-advertise-peer-urls", fmt.Sprintf("http://127.0.0.1:%d", etcdPeer),
		"--initial-cluster", fmt.Sprintf("default=http://127.0.0.1:%d", etcdPeer))
	etcdLog, _ := os.Create(filepath.Join(dir, "etcd.log"))
	etcd.Stdout, etcd.Stderr = etcdLog, etcdLog
	if err := etcd.Start(); err != nil {
		return nil, err
	}
	api := exec.Command(filepath.Join(assets, "kube-apiserver"),
		fmt.Sprintf("--etcd-servers=http://127.0.0.1:%d", etcdClient),
		"--cert-dir="+filepath.Join(dir, "certs"), fmt.Sprintf("--secure-port=%d", apiPort), "--bind-address=127.0.0.1",
		"--token-auth-file="+filepath.Join(dir, "tokens.csv"), "--authorization-mode=RBAC",
		"--service-account-issuer=https://kubernetes.default.svc",
		"--service-account-key-file="+filepath.Join(dir, "sa.pub"),
		"--service-account-signing-key-file="+filepath.Join(dir, "sa.key"),
		"--service-cluster-ip-range=10.96.0.0/16", "--allow-privileged=true")
	apiLog, _ := os.Create(filepath.Join(dir, "apiserver.log"))
	api.Stdout, api.Stderr = apiLog, apiLog
	if err := api.Start(); err != nil {
		etcd.Process.Kill()
		return nil, err
	}
	stop := func() { api.Process.Kill(); etcd.Process.Kill(); api.Wait(); etcd.Wait() }

	server := fmt.Sprintf("https://127.0.0.1:%d", apiPort)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	ready := false
	for i := 0; i < 120 && !ready; i++ {
		req, _ := http.NewRequest("GET", server+"/readyz", nil)
		req.Header.Set("Authorization", "Bearer e2etoken")
		if resp, err := client.Do(req); err == nil {
			ready = resp.StatusCode == 200
			resp.Body.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		stop()
		log, _ := os.ReadFile(filepath.Join(dir, "apiserver.log"))
		return nil, fmt.Errorf("kube-apiserver not ready:\n%s", tail(string(log), 20))
	}
	kubeconfig = filepath.Join(dir, "kubeconfig")
	os.WriteFile(kubeconfig, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters: [{name: e2e, cluster: {server: %q, insecure-skip-tls-verify: true}}]
users: [{name: admin, user: {token: e2etoken}}]
contexts: [{name: e2e, context: {cluster: e2e, user: admin}}]
current-context: e2e
`, server)), 0o600)
	os.Setenv("KUBECONFIG", kubeconfig)
	return stop, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// kubectl runs kubectl against the test API server.
func kubectl(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("kubectl", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func mustKubectl(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, err := kubectl(t, stdin, args...)
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// namespace creates a namespace with a default ServiceAccount (there is no
// controller-manager to create it).
func namespace(t *testing.T, name string) {
	mustKubectl(t, fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata: {name: %s}\n---\napiVersion: v1\nkind: ServiceAccount\nmetadata: {name: default, namespace: %s}\n", name, name), "apply", "-f", "-")
}

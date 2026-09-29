// Package kube fetches live resources from a cluster via kubectl.
package kube

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/k8sname"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// Get runs `kubectl get <args...> -o yaml` and returns the parsed resources.
// Cluster-managed fields (status, managedFields, uid, ...) are stripped so
// the result can be fixed and re-applied.
func Get(args []string, namespace, kubeContext string, allNamespaces bool) (*manifest.File, error) {
	// Resource arguments come from the command line or from MCP clients;
	// never let them be interpreted as kubectl flags.
	for _, a := range args {
		if err := k8sname.ValidResourceArg(a); err != nil {
			return nil, err
		}
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return nil, fmt.Errorf("kubectl not found in PATH")
	}
	cmdArgs := append([]string{"get"}, args...)
	if namespace != "" {
		if err := k8sname.ValidNamespace(namespace); err != nil {
			return nil, err
		}
		cmdArgs = append(cmdArgs, "--namespace="+namespace)
	}
	if allNamespaces {
		cmdArgs = append(cmdArgs, "--all-namespaces")
	}
	if kubeContext != "" {
		cmdArgs = append(cmdArgs, "--context="+kubeContext)
	}
	cmdArgs = append(cmdArgs, "-o", "yaml")
	var stderr bytes.Buffer
	cmd := exec.Command("kubectl", cmdArgs...) // #nosec G204 -- no shell; arguments validated by internal/k8sname above
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl %s: %v: %s", strings.Join(cmdArgs, " "), err, strings.TrimSpace(stderr.String()))
	}
	src := "cluster"
	if len(args) > 0 {
		src = "cluster:" + strings.Join(args, " ")
	}
	f, err := manifest.Parse(out, src)
	if err != nil {
		return nil, err
	}
	f.Writable = false
	// Secrets have nothing to audit but their values would be printed by
	// --fix (and could end up in logs), so their data never leaves here.
	for _, o := range f.Objects {
		if o.Kind() == "Secret" {
			yamlx.Delete(o.Root, "data")
			yamlx.Delete(o.Root, "stringData")
		}
		Clean(o)
	}
	return f, nil
}

// Clean removes server-populated fields from a live object.
func Clean(o *manifest.Object) {
	yamlx.Delete(o.Root, "status")
	if md := yamlx.Get(o.Root, "metadata"); md != nil {
		for _, k := range []string{"managedFields", "resourceVersion", "uid", "creationTimestamp", "generation", "selfLink"} {
			yamlx.Delete(md, k)
		}
		if ann := yamlx.Get(md, "annotations"); ann != nil {
			yamlx.Delete(ann, "kubectl.kubernetes.io/last-applied-configuration")
			yamlx.Delete(ann, "deployment.kubernetes.io/revision")
			if len(ann.Content) == 0 {
				yamlx.Delete(md, "annotations")
			}
		}
	}
}

package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/andronaft/k8s-guardian/internal/k8sname"
	"github.com/andronaft/k8s-guardian/internal/kube"
	"github.com/andronaft/k8s-guardian/internal/manifest"
)

// Rejection is the API server refusing an object: validation (an immutable
// field, an invalid value, a field the schema doesn't have) or admission (a
// ValidatingAdmissionPolicy, a webhook, a quota, Pod Security). Applying the
// object fails the same way.
type Rejection struct {
	Message string
}

func (r *Rejection) Error() string { return r.Message }

// DryRun is the result of a server-side dry run.
type DryRun struct {
	Object *manifest.Object
	// Warnings are the API server's warnings ("would violate PodSecurity
	// ...", deprecated APIs, admission policies in Warn mode).
	Warnings []string
}

// ErrForbidden means RBAC doesn't allow the request. A server-side dry run
// needs the patch permission on the resource.
var ErrForbidden = errors.New("forbidden by RBAC")

// DryRunApply runs `kubectl apply --server-side --dry-run=server`: the API
// server defaults, validates and admits o (webhooks and policies included)
// and returns what it would store, but stores nothing. The dedicated field
// manager and --force-conflicts make it preview the local values even where
// another manager owns a field. namespace is used for objects without one.
func (k *Kubectl) DryRunApply(o *manifest.Object, namespace string) (*DryRun, error) {
	args := []string{"apply", "--server-side", "--dry-run=server", "--force-conflicts", "--field-manager=k8s-guardian", "-o", "yaml", "-f", "-"}
	if namespace != "" {
		if err := k8sname.ValidNamespace(namespace); err != nil {
			return nil, err
		}
		args = append(args, "--namespace="+namespace)
	}
	in, err := yaml.Marshal(o.Root)
	if err != nil {
		return nil, err
	}
	out, stderr, err := k.exec(in, args...)
	if err != nil {
		return nil, dryRunError(stderr, err)
	}
	f, err := manifest.Parse(out, "dry-run")
	if err != nil {
		return nil, err
	}
	if len(f.Objects) != 1 {
		return nil, fmt.Errorf("dry run returned %d objects", len(f.Objects))
	}
	kube.Clean(f.Objects[0])
	return &DryRun{Object: f.Objects[0], Warnings: warnings(stderr)}, nil
}

// warnings returns the API server warnings kubectl printed.
func warnings(stderr string) []string {
	var out []string
	for _, l := range strings.Split(stderr, "\n") {
		if w, ok := strings.CutPrefix(strings.TrimSpace(l), "Warning: "); ok && w != "" {
			out = append(out, w)
		}
	}
	return out
}

// NamespaceLabels runs `kubectl get namespace/<name> -o json`.
func (k *Kubectl) NamespaceLabels(name string) (map[string]string, error) {
	if err := k8sname.ValidNamespace(name); err != nil {
		return nil, err
	}
	return cached(k, "ns-labels/"+name, func() (map[string]string, error) {
		out, err := k.run("get", "namespace/"+name, "-o", "json")
		if err != nil {
			return nil, err
		}
		var ns struct {
			Metadata meta `json:"metadata"`
		}
		if err := json.Unmarshal(out, &ns); err != nil {
			return nil, err
		}
		return ns.Metadata.Labels, nil
	})
}

var (
	serverError = regexp.MustCompile(`^Error from server(?: \((\w+)\))?: `)
	rbacDenied  = regexp.MustCompile(`cannot [a-z]+ resource "`)
	// `The Deployment "web" is invalid: `, `pods "web" is forbidden: `
	objectPrefix = regexp.MustCompile(`^(?:The )?\S+ "[^"]*" is (invalid|forbidden): (?:: )?`)
	typedPatch   = regexp.MustCompile(`^failed to create typed patch object \([^)]*\): `)
)

// dryRunError classifies kubectl's error output. Only refusals that applying
// would hit too are a *Rejection; RBAC, missing namespaces or CRDs, webhooks
// without dry-run support and anything unclear are plain errors.
func dryRunError(stderr string, err error) error {
	var msg string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "Warning:") {
			msg = l
			break
		}
	}
	if msg == "" {
		return fmt.Errorf("kubectl apply --dry-run=server: %v", err)
	}
	reason := ""
	if m := serverError.FindStringSubmatch(msg); m != nil {
		reason, msg = m[1], msg[len(m[0]):]
	}
	kind := ""
	if m := objectPrefix.FindStringSubmatch(msg); m != nil {
		kind = m[1]
	}
	switch {
	case strings.Contains(msg, "does not support dry run"):
		return errors.New(msg)
	case rbacDenied.MatchString(msg):
		return fmt.Errorf("%w: %s", ErrForbidden, msg)
	case reason == "NotFound":
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case kind == "invalid", kind == "forbidden", reason == "Forbidden", reason == "BadRequest",
		strings.Contains(msg, "denied the request"), strings.Contains(msg, "denied request"), typedPatch.MatchString(msg):
		msg = typedPatch.ReplaceAllString(objectPrefix.ReplaceAllString(msg, ""), "")
		if r := []rune(msg); len(r) > 300 {
			msg = string(r[:297]) + "..."
		}
		return &Rejection{Message: msg}
	}
	return errors.New(msg)
}

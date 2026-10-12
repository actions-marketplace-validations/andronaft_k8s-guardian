package cluster

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

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

// ErrForbidden means RBAC doesn't allow the request. A server-side dry run
// needs the patch permission on the resource.
var ErrForbidden = errors.New("forbidden by RBAC")

// DryRunApply runs `kubectl apply --server-side --dry-run=server`: the API
// server defaults, validates and admits o (webhooks and policies included)
// and returns what it would store, but stores nothing. The dedicated field
// manager and --force-conflicts make it preview the local values even where
// another manager owns a field. namespace is used for objects without one.
func (k *Kubectl) DryRunApply(o *manifest.Object, namespace string) (*manifest.Object, error) {
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
	return f.Objects[0], nil
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

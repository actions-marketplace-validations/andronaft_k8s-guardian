package rules

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// Default values used by the deterministic fixer.
const (
	DefaultCPURequest    = "100m"
	DefaultMemoryRequest = "128Mi"
	DefaultMemoryLimit   = "256Mi"
)

// All is the ordered list of built-in rules. Order matters for fixes: e.g.
// requests are added before limits so the limit can reuse the request value.
var All = []*Rule{
	{
		ID: "KG001", Name: "resource-requests", Severity: Error,
		Description: "Containers must declare CPU and memory requests so the scheduler can place them correctly.",
		Container: func(t *Target, c *Container) string {
			var missing []string
			for _, r := range []string{"cpu", "memory"} {
				if yamlx.Path(c.Node, "resources", "requests", r) == nil {
					missing = append(missing, r)
				}
			}
			if len(missing) == 0 {
				return ""
			}
			return fmt.Sprintf("missing resources.requests (%s)", strings.Join(missing, ", "))
		},
		Fix: func(t *Target, c *Container) bool {
			req := yamlx.EnsureMap(yamlx.EnsureMap(c.Node, "resources"), "requests")
			changed := false
			if yamlx.Get(req, "cpu") == nil {
				yamlx.Set(req, "cpu", yamlx.Str(DefaultCPURequest))
				changed = true
			}
			if yamlx.Get(req, "memory") == nil {
				yamlx.Set(req, "memory", yamlx.Str(DefaultMemoryRequest))
				changed = true
			}
			return changed
		},
	},
	{
		ID: "KG002", Name: "memory-limit", Severity: Error,
		Description: "Containers must declare a memory limit to avoid starving the node (OOM of neighbours).",
		Container: func(t *Target, c *Container) string {
			if yamlx.Path(c.Node, "resources", "limits", "memory") == nil {
				return "missing resources.limits.memory"
			}
			return ""
		},
		Fix: func(t *Target, c *Container) bool {
			res := yamlx.EnsureMap(c.Node, "resources")
			limit := DefaultMemoryLimit
			if req := yamlx.String(res, "requests", "memory"); req != "" {
				limit = req
			}
			yamlx.Set(yamlx.EnsureMap(res, "limits"), "memory", yamlx.Str(limit))
			return true
		},
	},
	{
		ID: "KG003", Name: "run-as-non-root", Severity: Error,
		Description: "Containers must run as a non-root user (securityContext.runAsNonRoot: true).",
		Container: func(t *Target, c *Container) string {
			if runsAsNonRoot(t, c) {
				return ""
			}
			return "securityContext.runAsNonRoot is not true"
		},
		Fix: func(t *Target, c *Container) bool {
			sc := c.EnsureSecurityContext()
			yamlx.Set(sc, "runAsNonRoot", yamlx.Bool(true))
			if u := yamlx.Get(sc, "runAsUser"); u != nil && u.Value == "0" {
				yamlx.Delete(sc, "runAsUser")
			}
			return true
		},
	},
	{
		ID: "KG004", Name: "no-privileged", Severity: Error,
		Description: "Privileged containers have full access to the host.",
		Container: func(t *Target, c *Container) string {
			if yamlx.IsTrue(yamlx.Get(c.SecurityContext(), "privileged")) {
				return "securityContext.privileged is true"
			}
			return ""
		},
		Fix: func(t *Target, c *Container) bool {
			yamlx.Set(c.EnsureSecurityContext(), "privileged", yamlx.Bool(false))
			return true
		},
	},
	{
		ID: "KG005", Name: "no-privilege-escalation", Severity: Warning,
		Description: "Set securityContext.allowPrivilegeEscalation: false to block setuid binaries from gaining privileges.",
		Container: func(t *Target, c *Container) string {
			if yamlx.IsFalse(yamlx.Get(c.SecurityContext(), "allowPrivilegeEscalation")) {
				return ""
			}
			return "securityContext.allowPrivilegeEscalation is not false"
		},
		Fix: func(t *Target, c *Container) bool {
			yamlx.Set(c.EnsureSecurityContext(), "allowPrivilegeEscalation", yamlx.Bool(false))
			return true
		},
	},
	{
		ID: "KG006", Name: "read-only-root-filesystem", Severity: Warning,
		Description: "Use securityContext.readOnlyRootFilesystem: true and mount writable paths as volumes.",
		Container: func(t *Target, c *Container) string {
			if yamlx.IsTrue(yamlx.Get(c.SecurityContext(), "readOnlyRootFilesystem")) {
				return ""
			}
			return "securityContext.readOnlyRootFilesystem is not true"
		},
		Fix: func(t *Target, c *Container) bool {
			yamlx.Set(c.EnsureSecurityContext(), "readOnlyRootFilesystem", yamlx.Bool(true))
			return true
		},
	},
	{
		ID: "KG007", Name: "drop-all-capabilities", Severity: Warning,
		Description: "Drop all Linux capabilities (securityContext.capabilities.drop: [ALL]) and add back only what is needed.",
		Container: func(t *Target, c *Container) string {
			drop := yamlx.Path(c.SecurityContext(), "capabilities", "drop")
			if drop != nil && drop.Kind == yaml.SequenceNode {
				for _, d := range drop.Content {
					if strings.EqualFold(d.Value, "ALL") {
						return ""
					}
				}
			}
			return "securityContext.capabilities.drop does not include ALL"
		},
		Fix: func(t *Target, c *Container) bool {
			caps := yamlx.EnsureMap(c.EnsureSecurityContext(), "capabilities")
			drop := yamlx.Get(caps, "drop")
			if drop == nil || drop.Kind != yaml.SequenceNode {
				yamlx.Set(caps, "drop", yamlx.Seq("ALL"))
				return true
			}
			drop.Content = append(drop.Content, yamlx.Str("ALL"))
			return true
		},
	},
	{
		ID: "KG008", Name: "liveness-probe", Severity: Warning,
		Description: "Long-running containers should define a livenessProbe so Kubernetes can restart hung processes.",
		Container: func(t *Target, c *Container) string {
			if c.Init || t.IsBatch() || yamlx.Get(c.Node, "livenessProbe") != nil {
				return ""
			}
			return "missing livenessProbe"
		},
	},
	{
		ID: "KG009", Name: "readiness-probe", Severity: Warning,
		Description: "Long-running containers should define a readinessProbe so traffic is only sent to ready pods.",
		Container: func(t *Target, c *Container) string {
			if c.Init || t.IsBatch() || yamlx.Get(c.Node, "readinessProbe") != nil {
				return ""
			}
			return "missing readinessProbe"
		},
	},
	{
		ID: "KG010", Name: "pinned-image-tag", Severity: Error,
		Description: "Images must use an explicit, immutable tag or digest (not :latest).",
		Container: func(t *Target, c *Container) string {
			img := yamlx.String(c.Node, "image")
			if img == "" {
				return "missing image"
			}
			if strings.Contains(img, "@sha256:") {
				return ""
			}
			tag := ""
			if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
				tag = img[i+1:]
			}
			switch tag {
			case "":
				return fmt.Sprintf("image %q has no tag (defaults to :latest)", img)
			case "latest":
				return fmt.Sprintf("image %q uses the mutable :latest tag", img)
			}
			return ""
		},
	},
	{
		ID: "KG011", Name: "no-host-namespaces", Severity: Error,
		Description: "Pods must not share the host network, PID or IPC namespaces.",
		Pod: func(t *Target) string {
			var on []string
			for _, k := range []string{"hostNetwork", "hostPID", "hostIPC"} {
				if yamlx.IsTrue(yamlx.Get(t.PodSpec, k)) {
					on = append(on, k)
				}
			}
			if len(on) == 0 {
				return ""
			}
			return strings.Join(on, ", ") + " enabled"
		},
		Fix: func(t *Target, _ *Container) bool {
			changed := false
			for _, k := range []string{"hostNetwork", "hostPID", "hostIPC"} {
				if yamlx.IsTrue(yamlx.Get(t.PodSpec, k)) {
					changed = yamlx.Delete(t.PodSpec, k) || changed
				}
			}
			return changed
		},
	},
	{
		ID: "KG012", Name: "no-host-path", Severity: Warning,
		Description: "hostPath volumes expose the node filesystem; prefer PVCs, emptyDir or ConfigMaps.",
		Pod: func(t *Target) string {
			vols := yamlx.Get(t.PodSpec, "volumes")
			if vols == nil {
				return ""
			}
			var names []string
			for _, v := range vols.Content {
				if yamlx.Get(v, "hostPath") != nil {
					names = append(names, yamlx.String(v, "name"))
				}
			}
			if len(names) == 0 {
				return ""
			}
			return "hostPath volumes: " + strings.Join(names, ", ")
		},
	},
	{
		ID: "KG013", Name: "seccomp-profile", Severity: Warning,
		Description: "Pods should use the RuntimeDefault seccomp profile (securityContext.seccompProfile.type).",
		Pod: func(t *Target) string {
			if hasSeccomp(t.PodSecurityContext()) {
				return ""
			}
			for _, c := range t.Containers {
				if !hasSeccomp(c.SecurityContext()) {
					return "securityContext.seccompProfile is not set to RuntimeDefault or Localhost"
				}
			}
			if len(t.Containers) == 0 {
				return "securityContext.seccompProfile is not set"
			}
			return ""
		},
		Fix: func(t *Target, _ *Container) bool {
			sc := yamlx.EnsureMap(t.PodSpec, "securityContext")
			yamlx.Set(yamlx.EnsureMap(sc, "seccompProfile"), "type", yamlx.Str("RuntimeDefault"))
			return true
		},
	},
	{
		ID: "KG014", Name: "automount-service-account-token", Severity: Info,
		Description: "Disable automountServiceAccountToken unless the workload talks to the Kubernetes API.",
		Pod: func(t *Target) string {
			if yamlx.IsFalse(yamlx.Get(t.PodSpec, "automountServiceAccountToken")) {
				return ""
			}
			return "automountServiceAccountToken is not false"
		},
	},
	{
		ID: "KG015", Name: "high-availability", Severity: Info,
		Description: "Deployments and StatefulSets should run at least 2 replicas for availability.",
		Pod: func(t *Target) string {
			k := t.Obj.Kind()
			if k != "Deployment" && k != "StatefulSet" {
				return ""
			}
			r := yamlx.Path(t.Obj.Root, "spec", "replicas")
			if r == nil {
				return "spec.replicas is not set (defaults to 1)"
			}
			if r.Value == "0" || r.Value == "1" {
				return fmt.Sprintf("spec.replicas is %s", r.Value)
			}
			return ""
		},
	},
}

func runsAsNonRoot(t *Target, c *Container) bool {
	csc, psc := c.SecurityContext(), t.PodSecurityContext()
	if v := yamlx.Get(csc, "runAsNonRoot"); v != nil {
		if yamlx.IsTrue(v) {
			return true
		}
		if yamlx.IsFalse(v) {
			return false
		}
	}
	if u := yamlx.Get(csc, "runAsUser"); u != nil {
		return u.Value != "0"
	}
	if yamlx.IsTrue(yamlx.Get(psc, "runAsNonRoot")) {
		return true
	}
	if u := yamlx.Get(psc, "runAsUser"); u != nil && u.Value != "0" {
		return true
	}
	return false
}

func hasSeccomp(sc *yaml.Node) bool {
	switch yamlx.String(sc, "seccompProfile", "type") {
	case "RuntimeDefault", "Localhost":
		return true
	}
	return false
}

// Lookup finds a rule by ID or name (case-insensitive).
func Lookup(idOrName string) *Rule {
	for _, r := range All {
		if strings.EqualFold(r.ID, idOrName) || strings.EqualFold(r.Name, idOrName) {
			return r
		}
	}
	return nil
}

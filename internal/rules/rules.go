package rules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/andronaft/k8s-guardian/internal/manifest"
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
		Unsafe: true, // guessed values
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
		Unsafe: true, // a guessed limit can OOM-kill the app
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
		Unsafe: true, // root images fail to start
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
			if isPrivileged(c) {
				return "securityContext.privileged is true"
			}
			return ""
		},
		// No auto-fix: privileged agents (CNI, CSI, node monitoring) need it,
		// and turning it off breaks them. Remove it by hand or ignore the
		// rule for that resource.
	},
	{
		ID: "KG005", Name: "no-privilege-escalation", Severity: Warning,
		Description: "Set securityContext.allowPrivilegeEscalation: false to block setuid binaries from gaining privileges.",
		Container: func(t *Target, c *Container) string {
			// Privileged containers can always escalate (KG004 reports them),
			// and the API rejects allowPrivilegeEscalation: false for them.
			if isPrivileged(c) || yamlx.IsFalse(yamlx.Get(c.SecurityContext(), "allowPrivilegeEscalation")) {
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
		Unsafe: true, // apps that write to their filesystem fail
		Fix: func(t *Target, c *Container) bool {
			yamlx.Set(c.EnsureSecurityContext(), "readOnlyRootFilesystem", yamlx.Bool(true))
			return true
		},
	},
	{
		ID: "KG007", Name: "drop-all-capabilities", Severity: Warning,
		Description: "Drop all Linux capabilities (securityContext.capabilities.drop: [ALL]) and add back only what is needed.",
		Container: func(t *Target, c *Container) string {
			if isPrivileged(c) {
				return "" // privileged containers have every capability (KG004)
			}
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
		Unsafe: true, // apps may need a capability (NET_BIND_SERVICE, ...)
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
		// No auto-fix: node agents use host namespaces on purpose.
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
		Description: "Deployments and StatefulSets should run at least 2 replicas (or an HPA with minReplicas >= 2) for availability.",
		Resource: func(o *manifest.Object, set *Set) []string {
			if !replicated(o) {
				return nil
			}
			// An HorizontalPodAutoscaler decides the replica count.
			if hpa := set.HPAFor(o); hpa != nil {
				min := yamlx.String(hpa.Root, "spec", "minReplicas")
				if min == "" {
					min = "1" // the API default
				}
				if n, err := strconv.Atoi(min); err == nil && n < 2 {
					return []string{fmt.Sprintf("HorizontalPodAutoscaler %s has minReplicas %d", hpa.Name(), n)}
				}
				return nil
			}
			r := yamlx.Path(o.Root, "spec", "replicas")
			if r == nil {
				return []string{"spec.replicas is not set (defaults to 1)"}
			}
			if r.Value == "0" || r.Value == "1" {
				return []string{fmt.Sprintf("spec.replicas is %s", r.Value)}
			}
			return nil
		},
	},
	{
		ID: "KG019", Name: "no-host-port", Severity: Warning,
		Description: "Containers should not bind host ports: only one such pod fits a node, and NetworkPolicies don't cover the port on many CNIs (the Pod Security baseline level forbids them).",
		Container: func(_ *Target, c *Container) string {
			var ports []string
			for _, p := range yamlx.Items(yamlx.Get(c.Node, "ports")) {
				if hp := yamlx.String(p, "hostPort"); hp != "" && hp != "0" {
					ports = append(ports, hp)
				}
			}
			if len(ports) == 0 {
				return ""
			}
			return "binds hostPort " + strings.Join(ports, ", ") + " on the node"
		},
	},
	{
		ID: "KG021", Name: "literal-secret", Severity: Warning,
		Description: "Credentials must not be literal env values in the manifest (they end up in Git and in kubectl output); read them from a Secret with valueFrom.secretKeyRef or from a mounted file.",
		Container: func(_ *Target, c *Container) string {
			var names []string
			for _, e := range yamlx.Items(yamlx.Get(c.Node, "env")) {
				name := yamlx.String(e, "name")
				v := yamlx.Get(e, "value")
				if v == nil || v.Kind != yaml.ScalarNode || !looksLikeSecret(name, v.Value) {
					continue
				}
				names = append(names, name)
			}
			if len(names) == 0 {
				return ""
			}
			// The value is never part of the finding.
			return "env " + strings.Join(names, ", ") + " holds a literal credential: use valueFrom.secretKeyRef or a mounted file"
		},
	},
}

var (
	secretName = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|API_?KEY|PRIVATE_?KEY)`)
	// Names that hold a location, an identifier or a setting, not the
	// credential itself.
	secretNameNot = regexp.MustCompile(`(?i)(_FILE|_PATH|_DIR|_NAME|_REF|_URL|_URI|_ID|_HEADER|_ENABLED|_REQUIRED|_TTL|_EXPIRY|_LIFETIME|_AUDIENCE|_ISSUER|_ALG|_ALGORITHM|_LENGTH|_FORMAT|_TYPE|_MODE|_POLICY|_SOURCE|_PROVIDER|_VERSION|_ROUNDS)$`)
	// Values that are plainly not a credential: switches, numbers,
	// placeholders, templates, paths and URLs.
	secretPlaceholder = regexp.MustCompile(`(?i)^(yes|no|on|off|true|false|none|null|\d{1,3}|change[-_]?me|password|passw0rd|secret\d*|x{3,}|placeholder|example|test|dummy|your[-_ ].*|<.*>|\[.*\]|\$\{.*\}|\$\(.*\)|\{\{.*\}\}|%\(.*\)s|#\{.*\}|/.*|\./.*|~.*|https?://.*|.*:\d+)$`)
)

// HasLiteralSecret reports whether a container of objs sets an env var that
// looks like a credential to a literal value (what KG021 reports), whether
// or not the rule is enabled: callers use it to keep such manifests off the
// network.
func HasLiteralSecret(objs []*manifest.Object) bool {
	for _, o := range objs {
		t := NewTarget(o)
		if t == nil {
			continue
		}
		for _, c := range t.Containers {
			for _, e := range yamlx.Items(yamlx.Get(c.Node, "env")) {
				if v := yamlx.Get(e, "value"); v != nil && v.Kind == yaml.ScalarNode && looksLikeSecret(yamlx.String(e, "name"), v.Value) {
					return true
				}
			}
		}
	}
	return false
}

// looksLikeSecret reports whether env var name with the literal value holds
// a credential. The heuristic was checked by hand on 30 Helm charts and six
// example repositories (issue #17): every hit it keeps was a real password
// or key written into the manifest.
func looksLikeSecret(name, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !secretName.MatchString(name) || secretNameNot.MatchString(name) {
		return false
	}
	if secretPlaceholder.MatchString(value) || strings.Contains(strings.ToLower(value), strings.ToLower(name)) {
		return false // a switch, a placeholder, or "INSERT_<NAME>_HERE"
	}
	return true
}

// replicated reports whether o is a workload whose replica count matters:
// a Deployment, a StatefulSet or an Argo Rollout.
func replicated(o *manifest.Object) bool {
	switch o.Kind() {
	case "Deployment", "StatefulSet":
		return true
	case "Rollout":
		return strings.HasPrefix(o.APIVersion(), "argoproj.io/")
	}
	return false
}

func isPrivileged(c *Container) bool {
	return yamlx.IsTrue(yamlx.Get(c.SecurityContext(), "privileged"))
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

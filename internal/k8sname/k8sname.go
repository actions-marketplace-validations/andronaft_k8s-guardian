// Package k8sname validates values before they reach the kubectl command line.
package k8sname

import (
	"fmt"
	"regexp"
)

// Names that reach kubectl's command line come from manifests (possibly an
// untrusted pull request) or from MCP clients. Anything that could be parsed
// as a flag (e.g. namespace "--server=https://attacker") would let a
// manifest redirect kubectl, and the user's credentials, to another server,
// so every value is validated against Kubernetes naming rules first.
var (
	nsRe     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)              // DNS-1123 label
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._:@-]*[A-Za-z0-9])?$`) // object names incl. RBAC "system:..."
	kindRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	groupRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	labelKey = regexp.MustCompile(`^([a-z0-9]([-a-z0-9.]*[a-z0-9])?/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	labelVal = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`)
	// ResourceArg matches kubectl resource arguments such as "deployment/app",
	// "deployments,statefulsets" or "deployment.apps/app".
	resourceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.,/_:-]*$`)
)

// ErrInvalidName is returned for values that are not valid Kubernetes names.
type ErrInvalidName struct{ What, Value string }

func (e *ErrInvalidName) Error() string {
	return fmt.Sprintf("invalid %s %q: not a valid Kubernetes name, refusing to pass it to kubectl", e.What, e.Value)
}

// ValidNamespace checks a namespace name.
func ValidNamespace(ns string) error {
	if len(ns) > 63 || !nsRe.MatchString(ns) {
		return &ErrInvalidName{"namespace", ns}
	}
	return nil
}

// ValidName checks an object name.
func ValidName(name string) error {
	if len(name) > 253 || !nameRe.MatchString(name) {
		return &ErrInvalidName{"name", name}
	}
	return nil
}

// ValidKind checks a kind and apiVersion group.
func ValidKind(kind, group string) error {
	if !kindRe.MatchString(kind) {
		return &ErrInvalidName{"kind", kind}
	}
	if group != "" && !groupRe.MatchString(group) {
		return &ErrInvalidName{"API group", group}
	}
	return nil
}

// ValidSelector checks label selector keys and values.
func ValidSelector(sel map[string]string) error {
	for k, v := range sel {
		if len(k) > 316 || !labelKey.MatchString(k) {
			return &ErrInvalidName{"label key", k}
		}
		if len(v) > 63 || !labelVal.MatchString(v) {
			return &ErrInvalidName{"label value", v}
		}
	}
	return nil
}

// ValidResourceArg checks a kubectl resource argument (audit / MCP).
func ValidResourceArg(arg string) error {
	if len(arg) > 512 || !resourceRe.MatchString(arg) {
		return &ErrInvalidName{"resource", arg}
	}
	return nil
}

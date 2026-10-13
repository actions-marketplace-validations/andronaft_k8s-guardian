package rules

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/manifest"
)

// FuzzValidateAndFix feeds arbitrary YAML through parsing, every rule,
// --fix --unsafe-fixes and encoding. Manifests come from pull requests and
// MCP clients, so no input may panic, and fixing must be idempotent.
func FuzzValidateAndFix(f *testing.F) {
	seeds, _ := filepath.Glob("../../examples/*.yaml")
	for _, p := range seeds {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("kind: Service\nspec: {selector: {app: x}, ports: [{targetPort: 8080}]}\n---\nkind: Deployment\nspec: {template: {metadata: {labels: {app: x}}}}\n"))
	f.Add([]byte("apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nrules: [{verbs: ['*'], resources: [x]}, 7, ~]\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := manifest.Parse(data, "fuzz.yaml")
		if err != nil {
			return
		}
		opts := NewOptions("")
		opts.UnsafeFixes = true
		Validate(file.Objects, opts)
		Fix(file.Objects, opts)
		Validate(file.Objects, opts)
		once, err := file.Encode()
		if err != nil {
			return
		}
		again, err := manifest.Parse(once, "fuzz.yaml")
		if err != nil {
			t.Fatalf("fixed output doesn't parse: %v\n%s", err, once)
		}
		Fix(again.Objects, opts)
		twice, err := again.Encode()
		if err == nil && !bytes.Equal(once, twice) {
			t.Fatalf("fix is not idempotent:\n%s\n---\n%s", once, twice)
		}
	})
}

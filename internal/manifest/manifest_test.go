package manifest

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

const stream = `# header comment
---
apiVersion: v1
kind: ConfigMap
metadata: {name: cfg}
data:
  config.yaml: |

    starts: with an empty line
  filters: >-
    [a,b]
    [c,d]
---
# Source: chart/templates/pdb.yaml
# a template that rendered to comments only
---
# Source: chart/templates/deploy.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
      - name: web   # inline comment
        image: nginx:1.27
        args:
        - --port=8080
`

func parse(t *testing.T, s string) *File {
	t.Helper()
	f, err := Parse([]byte(s), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func encode(t *testing.T, f *File) string {
	t.Helper()
	out, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestEncodeKeepsUnmodifiedDocumentsVerbatim(t *testing.T) {
	f := parse(t, stream)
	// The comment-only first chunk is kept with the document that follows it.
	want := strings.Replace(stream, "# header comment\n---\n", "# header comment\n", 1)
	if got := encode(t, f); got != want {
		t.Fatalf("round trip changed the stream:\n%s", got)
	}
}

func TestEncodeRewritesOnlyModifiedDocuments(t *testing.T) {
	f := parse(t, stream)
	var deploy *Object
	for _, o := range f.Objects {
		if o.Kind() == "Deployment" {
			deploy = o
		}
	}
	yamlx.Set(yamlx.Path(deploy.Root, "metadata"), "namespace", yamlx.Str("prod"))
	got := encode(t, f)
	cm := strings.SplitN(got, "---\n", 2)[0]
	if !strings.Contains(cm, "  config.yaml: |\n\n    starts") || !strings.Contains(cm, "    [a,b]\n    [c,d]\n") {
		t.Errorf("unmodified ConfigMap was reformatted:\n%s", cm)
	}
	if !strings.Contains(got, "namespace: prod") || !strings.Contains(got, "# inline comment") {
		t.Errorf("modified document lost the change or its comments:\n%s", got)
	}
}

func TestEncodeKeepsLeadingNewlineInModifiedBlockScalar(t *testing.T) {
	f := parse(t, stream)
	cm := f.Objects[0]
	yamlx.Set(yamlx.Path(cm.Root, "metadata"), "namespace", yamlx.Str("prod"))
	var back struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(strings.SplitN(encode(t, f), "---\n", 2)[0]), &back); err != nil {
		t.Fatal(err)
	}
	if want := "\nstarts: with an empty line\n"; back.Data["config.yaml"] != want {
		t.Errorf("config.yaml = %q, want %q", back.Data["config.yaml"], want)
	}
}

func TestEncodeFallsBackForUnusualStreams(t *testing.T) {
	// Content on the "---" line can't be split by line; everything is re-encoded.
	f := parse(t, "--- {kind: ConfigMap, apiVersion: v1, metadata: {name: a}}\n---\nkind: Secret\napiVersion: v1\nmetadata: {name: b}\n")
	if f.raw != nil {
		t.Fatal("expected the verbatim mode to be disabled")
	}
	if got := encode(t, f); !strings.Contains(got, "name: a") || !strings.Contains(got, "name: b") {
		t.Errorf("documents lost:\n%s", got)
	}
}

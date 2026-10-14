package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/andronaft/k8s-guardian/internal/rules"
)

// testdata/sarif-schema-2.1.0.json is the official SARIF 2.1.0 schema
// (https://github.com/oasis-tcs/sarif-spec, Schemata/sarif-schema-2.1.0.json).
func TestSARIFIsValidAndDescribesEveryRule(t *testing.T) {
	custom := &rules.Rule{ID: "ORG001", Name: "contact-annotation", Severity: rules.Warning, Description: "Workloads carry a contact annotation.", Custom: true}
	outside := filepath.Join(filepath.Dir(t.TempDir()), "other repo", "app.yaml")
	inside, err := filepath.Abs(filepath.Join("k8s", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	findings := []rules.Finding{
		{RuleID: "KG001", Rule: "resource-requests", Severity: rules.Error, Message: "missing", Source: filepath.Join("k8s", "deploy.yaml"), Resource: "Deployment/web", Line: 7},
		{RuleID: "ORG001", Rule: custom.Name, Severity: rules.Warning, Message: "no contact", Source: inside, Resource: "Deployment/web"},
		{RuleID: "LV003", Rule: "node-fit", Severity: rules.Error, Message: "too big", Source: outside, Resource: "Deployment/web"},
	}
	var buf bytes.Buffer
	if err := Write(&buf, "sarif", findings, Summarize(findings), false, custom); err != nil {
		t.Fatal(err)
	}

	schema, err := jsonschema.UnmarshalJSON(mustOpen(t, "testdata/sarif-schema-2.1.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("sarif.json", schema); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("sarif.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(doc); err != nil {
		t.Fatalf("SARIF doesn't validate: %v\n%s", err, buf.String())
	}

	var out struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Rules []struct {
						ID               string                `json:"id"`
						ShortDescription struct{ Text string } `json:"shortDescription"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string `json:"ruleId"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct{ URI string } `json:"artifactLocation"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	run := out.Runs[0]
	described := map[string]string{}
	for _, r := range run.Tool.Driver.Rules {
		described[r.ID] = r.ShortDescription.Text
	}
	if described["ORG001"] != custom.Description {
		t.Errorf("custom rule described as %q", described["ORG001"])
	}
	var uris []string
	for _, r := range run.Results {
		if _, ok := described[r.RuleID]; !ok {
			t.Errorf("result %s has no rule descriptor", r.RuleID)
		}
		uris = append(uris, r.Locations[0].PhysicalLocation.ArtifactLocation.URI)
	}
	if uris[0] != "k8s/deploy.yaml" || uris[1] != "k8s/app.yaml" {
		t.Errorf("paths in the working directory must be relative with /: %q", uris[:2])
	}
	if !strings.HasPrefix(uris[2], "file:///") || !strings.HasSuffix(uris[2], "/other%20repo/app.yaml") || (runtime.GOOS == "windows" && strings.Contains(uris[2], `\`)) {
		t.Errorf("a path outside the working directory must be a file URI: %q", uris[2])
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

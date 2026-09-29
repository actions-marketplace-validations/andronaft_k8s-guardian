package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/rules"
)

const fixPrompt = `You are k8s-guardian, a senior Kubernetes platform and security engineer.
You receive Kubernetes manifests together with policy findings and return a corrected version of the manifests.

Rules for your answer:
- Return the COMPLETE corrected YAML stream (all documents, separated by ---) inside exactly one fenced ` + "```yaml" + ` block, followed by a short bullet list explaining each change.
- Fix every listed finding. Keep all unrelated fields, document order, key order and YAML comments intact.
- Choose realistic values from context: probes should target a declared containerPort (httpGet for HTTP-looking ports/names such as http, web, 80, 8080, otherwise tcpSocket) with sensible initialDelaySeconds/periodSeconds; resource values should fit the workload type.
- Never invent credentials, hostnames or image versions you cannot infer. When a safe value cannot be determined (for example the exact image version to pin), keep the field as is and add a YAML comment starting with "# TODO(k8s-guardian):" explaining what the user must decide.
- If readOnlyRootFilesystem is enabled, add emptyDir volumes for paths the image obviously needs to write (for example /tmp, nginx cache/run directories).
- Output must be valid Kubernetes YAML that can be applied with kubectl.`

var fence = regexp.MustCompile("(?s)```(?:yaml|yml)?\\s*\\n(.*?)```")

// Fix returns the corrected manifest and Claude's explanation.
func (c *Client) Fix(ctx context.Context, manifest string, findings []rules.Finding) (fixed, notes string, err error) {
	var b strings.Builder
	b.WriteString("Findings reported by k8s-guardian:\n")
	for _, fd := range findings {
		fmt.Fprintf(&b, "- [%s %s/%s] %s", fd.Severity, fd.RuleID, fd.Rule, fd.Resource)
		if fd.Container != "" {
			fmt.Fprintf(&b, " container %q", fd.Container)
		}
		fmt.Fprintf(&b, ": %s\n", fd.Message)
	}
	b.WriteString("\nManifest:\n```yaml\n")
	b.WriteString(manifest)
	b.WriteString("\n```\n")

	out, err := c.request(ctx, fixPrompt, []anthropic.MessageParam{userText(b.String())}, nil)
	if err != nil {
		return "", "", err
	}
	m := fence.FindStringSubmatchIndex(out)
	if m == nil {
		return "", "", errors.New("no YAML block in the answer from Claude")
	}
	fixed = out[m[2]:m[3]]
	notes = strings.TrimSpace(out[m[1]:])

	// Make sure the answer is at least syntactically valid YAML.
	dec := yaml.NewDecoder(strings.NewReader(fixed))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", "", fmt.Errorf("invalid YAML in the answer from Claude: %w", err)
		}
	}
	return fixed, notes, nil
}

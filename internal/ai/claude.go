// Package ai asks Claude to remediate findings that cannot be fixed
// deterministically (probes, image tags, context-dependent settings).
package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/rules"
)

// DefaultModel is used unless --model or K8S_GUARDIAN_MODEL is set.
const DefaultModel = "claude-opus-5-5"

// Model returns the model to use, honouring K8S_GUARDIAN_MODEL.
func Model(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if m := os.Getenv("K8S_GUARDIAN_MODEL"); m != "" {
		return m
	}
	return DefaultModel
}

const systemPrompt = `You are k8s-guardian, a senior Kubernetes platform and security engineer.
You receive Kubernetes manifests together with policy findings and return a corrected version of the manifests.

Rules for your answer:
- Return the COMPLETE corrected YAML stream (all documents, separated by ---) inside exactly one fenced ` + "```yaml" + ` block, followed by a short bullet list explaining each change.
- Fix every listed finding. Keep all unrelated fields, document order, key order and YAML comments intact.
- Choose realistic values from context: probes should target a declared containerPort (httpGet for HTTP-looking ports/names such as http, web, 80, 8080, otherwise tcpSocket) with sensible initialDelaySeconds/periodSeconds; resource values should fit the workload type.
- Never invent credentials, hostnames or image versions you cannot infer. When a safe value cannot be determined (for example the exact image version to pin), keep the field as is and add a YAML comment starting with "# TODO(k8s-guardian):" explaining what the user must decide.
- If readOnlyRootFilesystem is enabled, add emptyDir volumes for paths the image obviously needs to write (for example /tmp, nginx cache/run directories).
- Output must be valid Kubernetes YAML that can be applied with kubectl.`

// Fixer calls the Claude API.
type Fixer struct {
	client anthropic.Client
	model  string
}

// New creates a Fixer. Credentials are resolved by the Anthropic SDK
// (ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or an `ant auth login` profile).
func New(model string) *Fixer {
	return &Fixer{client: anthropic.NewClient(), model: Model(model)}
}

var fence = regexp.MustCompile("(?s)```(?:yaml|yml)?\\s*\\n(.*?)```")

// Fix returns the corrected manifest and Claude's explanation.
func (f *Fixer) Fix(ctx context.Context, manifest string, findings []rules.Finding) (fixed, notes string, err error) {
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

	stream := f.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:        anthropic.Model(f.model),
		MaxTokens:    64000,
		System:       []anthropic.TextBlockParam{{Text: systemPrompt}},
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortHigh},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(b.String())),
		},
	},
		// Server-side fallback: if a request is declined by a safety
		// classifier, the API re-serves it on a suitable fallback model.
		option.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
		option.WithJSONSet("fallbacks", "default"),
	)
	msg := anthropic.Message{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return "", "", err
		}
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
			return "", "", fmt.Errorf("Claude API authentication failed: set ANTHROPIC_API_KEY (or run `ant auth login`): %w", err)
		}
		return "", "", fmt.Errorf("Claude API: %w", err)
	}
	switch msg.StopReason {
	case anthropic.StopReasonRefusal:
		return "", "", errors.New("Claude declined to process this manifest")
	case anthropic.StopReasonMaxTokens:
		return "", "", errors.New("Claude response was truncated (manifest too large); fix files individually")
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	out := text.String()
	m := fence.FindStringSubmatchIndex(out)
	if m == nil {
		return "", "", errors.New("Claude did not return a YAML block")
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
			return "", "", fmt.Errorf("Claude returned invalid YAML: %w", err)
		}
	}
	return fixed, notes, nil
}

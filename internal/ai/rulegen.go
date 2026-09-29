package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/andronaft/k8s-guardian/internal/custom"
)

const rulePrompt = `You translate natural-language Kubernetes policies into k8s-guardian custom rules.

A rule has:
- name: kebab-case, descriptive (e.g. require-contact-annotation)
- id: short unique uppercase id such as ORG001, ORG002 (never starting with KG)
- severity: error | warning | info
- description: one sentence explaining the policy and why it matters
- message: the text shown when a resource violates the rule
- scope: where paths are evaluated
    resource  -> relative to the whole object (metadata.labels, spec.replicas, ...)
    pod       -> relative to the pod spec of workloads (hostNetwork, serviceAccountName, securityContext.runAsUser, volumes[*].hostPath ...)
    container -> relative to each container and init container (image, resources.limits.cpu, ports[*].containerPort ...)
- kinds: kinds the rule applies to (empty = all kinds; pod/container scopes only ever see workloads)
- when: optional preconditions; if any is false the rule is skipped for that object
- assert: conditions that must ALL hold; the first failing one is a violation

Paths use dots, ['quoted keys'] for keys containing dots or slashes, [N] for an index and [*] for every element.
Example: metadata.annotations['example.com/owner'], spec.template.spec.containers[*].image

Operators (op): exists, notExists, equals, notEquals (value), in, notIn (values),
matches, notMatches (value is a Go RE2 regex), gt, gte, lt, lte (value is a number or Kubernetes quantity such as 500m or 2Gi).
Missing fields satisfy notEquals/notIn/notMatches and numeric comparisons; add an "exists" assertion to require the field.
Use empty strings / empty arrays for fields that do not apply.

Split a request that contains several independent policies into several rules.`

func ruleSchema() map[string]any {
	cond := object(map[string]any{
		"path":   map[string]any{"type": "string"},
		"op":     map[string]any{"type": "string", "enum": custom.Ops},
		"value":  map[string]any{"type": "string"},
		"values": strArray(),
	})
	condArray := map[string]any{"type": "array", "items": cond}
	rule := object(map[string]any{
		"name":        map[string]any{"type": "string"},
		"id":          map[string]any{"type": "string"},
		"severity":    map[string]any{"type": "string", "enum": []string{"error", "warning", "info"}},
		"description": map[string]any{"type": "string"},
		"message":     map[string]any{"type": "string"},
		"scope":       map[string]any{"type": "string", "enum": []string{"resource", "pod", "container"}},
		"kinds":       strArray(),
		"when":        condArray,
		"assert":      condArray,
	})
	return object(map[string]any{"rules": map[string]any{"type": "array", "items": rule}})
}

type generatedRule struct {
	Name        string             `json:"name"`
	ID          string             `json:"id"`
	Severity    string             `json:"severity"`
	Description string             `json:"description"`
	Message     string             `json:"message"`
	Scope       string             `json:"scope"`
	Kinds       []string           `json:"kinds"`
	When        []custom.Condition `json:"when"`
	Assert      []custom.Condition `json:"assert"`
}

// GenerateRules turns a natural-language policy into custom rule documents.
// Every generated rule is compiled; on errors Claude gets one chance to
// correct them.
func (c *Client) GenerateRules(ctx context.Context, policy string, existingIDs []string) ([]custom.Document, error) {
	prompt := "Policy:\n" + policy
	if len(existingIDs) > 0 {
		prompt += "\n\nThese rule IDs are already taken: " + strings.Join(existingIDs, ", ")
	}
	msgs := []anthropic.MessageParam{userText(prompt)}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := c.request(ctx, rulePrompt, msgs, ruleSchema())
		if err != nil {
			return nil, err
		}
		docs, err := decodeRules(out, existingIDs)
		if err == nil {
			return docs, nil
		}
		lastErr = err
		msgs = append(msgs,
			anthropic.NewAssistantMessage(anthropic.NewTextBlock(out)),
			userText("These rules failed validation: "+err.Error()+"\nReturn the corrected rules."))
	}
	return nil, fmt.Errorf("generated rules are invalid: %w", lastErr)
}

func decodeRules(out string, existingIDs []string) ([]custom.Document, error) {
	var resp struct {
		Rules []generatedRule `json:"rules"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(resp.Rules) == 0 {
		return nil, fmt.Errorf("no rules returned")
	}
	taken := map[string]bool{}
	for _, id := range existingIDs {
		taken[id] = true
	}
	var docs []custom.Document
	var errs []string
	for _, g := range resp.Rules {
		d := custom.Document{
			APIVersion: custom.APIVersion, Kind: custom.Kind,
			Metadata: custom.Metadata{Name: g.Name},
			Spec: custom.Spec{
				ID: g.ID, Severity: g.Severity, Description: g.Description, Message: g.Message,
				Match: custom.Match{Kinds: g.Kinds, Scope: g.Scope}, When: g.When, Assert: g.Assert,
			},
		}
		if taken[g.ID] {
			errs = append(errs, fmt.Sprintf("rule %s: id %s is already taken", g.Name, g.ID))
			continue
		}
		taken[g.ID] = true
		if _, err := custom.Compile(d); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		docs = append(docs, d)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return docs, nil
}

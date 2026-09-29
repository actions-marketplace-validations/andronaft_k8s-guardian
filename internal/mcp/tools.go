package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/cost"
	"github.com/andronaft/k8s-guardian/internal/custom"
	"github.com/andronaft/k8s-guardian/internal/diff"
	"github.com/andronaft/k8s-guardian/internal/live"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/quantity"
	"github.com/andronaft/k8s-guardian/internal/report"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

type toolArgs struct {
	YAML      string `json:"yaml"`
	Skip      string `json:"skip"`
	UseAI     bool   `json:"use_ai"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Rule      string `json:"rule"`
}

const customRuleHelp = `Rule format (YAML):
apiVersion: k8s-guardian.io/v1
kind: Rule
metadata: {name: require-owner-label}
spec:
  id: ORG001                 # unique, must not start with KG
  severity: error            # error | warning | info
  description: Every workload needs an owner label.
  message: add metadata.labels.owner   # optional
  match:
    kinds: [Deployment, StatefulSet]   # optional, empty = all kinds
    scope: resource          # resource (object) | pod (pod spec) | container (each container)
  when:                      # optional preconditions
    - {path: spec.replicas, op: gte, value: "2"}
  assert:                    # all must hold
    - {path: "metadata.labels.owner", op: exists}
Paths: dots, ['quoted.keys'], [N], [*]. Ops: exists, notExists, equals, notEquals, in, notIn (values: [...]),
matches, notMatches (RE2 regex), gt, gte, lt, lte (numbers or quantities like 500m, 2Gi).
Save rules under .k8s-guardian/rules/ to enforce them in check, the TUI and this server.`

var tools = append(baseTools,
	map[string]any{
		"name":        "check_live",
		"description": "Validate Kubernetes YAML against the live cluster of the current kubectl context: served APIs/CRDs, namespace existence, node capacity, ResourceQuota headroom, LimitRanges, and whether referenced ConfigMaps/Secrets/ServiceAccounts/PVCs/StorageClasses exist. Answers 'will this actually run there?'.",
		"inputSchema": schema(map[string]any{
			"yaml":      map[string]string{"type": "string", "description": "Kubernetes manifest YAML"},
			"namespace": map[string]string{"type": "string", "description": "Namespace for objects without one (optional)"},
		}, "yaml"),
	},
	map[string]any{
		"name":        "diff_cluster",
		"description": "Compare Kubernetes YAML with what is deployed in the cluster and flag breaking changes: immutable selectors/fields, removed APIs for the cluster version, downtime (Recreate), Service port changes, removed containers.",
		"inputSchema": schema(map[string]any{
			"yaml":      map[string]string{"type": "string", "description": "Kubernetes manifest YAML"},
			"namespace": map[string]string{"type": "string", "description": "Namespace for objects without one (optional)"},
		}, "yaml"),
	},
	map[string]any{
		"name":        "estimate_cost",
		"description": "Estimate the monthly cloud cost of the workloads in Kubernetes YAML from their resource requests (HPA aware). Use it to sanity-check requests before proposing them.",
		"inputSchema": schema(map[string]any{
			"yaml": map[string]string{"type": "string", "description": "Kubernetes manifest YAML"},
		}, "yaml"),
	},
	map[string]any{
		"name":        "validate_custom_rule",
		"description": "Validate a k8s-guardian custom rule (YAML) and optionally test it against a manifest. Use it to author organisation policies. " + customRuleHelp,
		"inputSchema": schema(map[string]any{
			"rule": map[string]string{"type": "string", "description": "Custom rule YAML (one or more documents)"},
			"yaml": map[string]string{"type": "string", "description": "Optional Kubernetes YAML to test the rule against"},
		}, "rule"),
	},
)

// loadOptions includes custom rules from .k8s-guardian/rules and $K8S_GUARDIAN_RULES.
func loadOptions(skip string) (rules.Options, error) {
	opts := rules.NewOptions(skip)
	paths := []string{custom.DefaultDir}
	if env := os.Getenv("K8S_GUARDIAN_RULES"); env != "" {
		paths = append(paths, filepath.SplitList(env)...)
	}
	cr, err := custom.Load(paths)
	if err != nil {
		return opts, fmt.Errorf("custom rules: %w", err)
	}
	opts.Custom = cr
	return opts, nil
}

func (s *Server) callTool(ctx context.Context, name string, raw json.RawMessage) (string, bool) {
	var args toolArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "invalid arguments: " + err.Error(), true
		}
	}
	switch name {
	case "list_rules":
		opts, err := loadOptions("")
		if err != nil {
			return err.Error(), true
		}
		var b strings.Builder
		for _, r := range append(opts.Rules(), append(live.Rules, diff.Rules...)...) {
			fix := "manual/AI"
			if r.Fix != nil {
				fix = "auto"
			}
			fmt.Fprintf(&b, "%-7s %-32s %-7s fix:%-9s %s\n", r.ID, r.Name, r.Severity, fix, r.Description)
		}
		return b.String(), false
	case "check_live", "diff_cluster":
		opts, err := loadOptions(args.Skip)
		if err != nil {
			return err.Error(), true
		}
		f, err := manifest.Parse([]byte(args.YAML), "input.yaml")
		if err != nil {
			return err.Error(), true
		}
		cl, err := cluster.NewKubectl("")
		if err != nil {
			return err.Error(), true
		}
		if name == "check_live" {
			res := live.Check(cl, f.Objects, args.Namespace, opts)
			out := report.Text(res.Findings)
			if res.ClusterVersion != "" {
				out = "Cluster " + res.ClusterVersion + "\n" + out
			}
			for _, n := range res.Notes {
				out += "note: " + n + "\n"
			}
			return out, false
		}
		res := diff.Run(cl, f.Objects, args.Namespace, opts)
		b, _ := json.MarshalIndent(res, "", "  ")
		return string(b), false
	case "estimate_cost":
		f, err := manifest.Parse([]byte(args.YAML), "input.yaml")
		if err != nil {
			return err.Error(), true
		}
		p := cost.DefaultPricing()
		var b strings.Builder
		var total float64
		for _, w := range cost.Estimate(f.Objects, p) {
			fmt.Fprintf(&b, "%s: %d-%d replica(s) × (cpu %s, memory %s) ≈ $%.2f-$%.2f/month\n", w.Resource, w.MinReplicas, w.MaxReplicas,
				quantity.FormatCPU(w.PodCPU), quantity.FormatBytes(w.PodMemory), w.MonthlyMin, w.MonthlyMax)
			for _, n := range w.Notes {
				fmt.Fprintf(&b, "  - %s\n", n)
			}
			total += w.MonthlyMin
		}
		fmt.Fprintf(&b, "Total (min replicas): $%.2f/month at $%.4f/vCPU-h and $%.4f/GiB-h\n", total, p.CPUHour, p.GiBHour)
		return b.String(), false
	case "validate_custom_rule":
		docs, err := custom.Parse([]byte(args.Rule), "rule.yaml")
		if err != nil {
			return err.Error(), true
		}
		var compiled []*rules.Rule
		for _, d := range docs {
			r, err := custom.Compile(d)
			if err != nil {
				return err.Error(), true
			}
			compiled = append(compiled, r)
		}
		out := fmt.Sprintf("✔ %d rule(s) valid\n", len(compiled))
		if args.YAML != "" {
			f, err := manifest.Parse([]byte(args.YAML), "input.yaml")
			if err != nil {
				return err.Error(), true
			}
			test := rules.Options{Skip: map[string]bool{}, Custom: compiled}
			for _, r := range rules.All {
				test.Skip[strings.ToLower(r.ID)] = true
			}
			out += "\nTest against the manifest:\n" + report.Text(rules.Validate(f.Objects, test))
		}
		return out, false
	}
	return s.callBaseTool(ctx, name, args)
}

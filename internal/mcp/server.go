// Package mcp implements a Model Context Protocol server (JSON-RPC 2.0 over
// stdio) exposing k8s-guardian to AI coding agents such as Claude Code and
// Cursor.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/andronaft/k8s-guardian/internal/guardian"
	"github.com/andronaft/k8s-guardian/internal/kube"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/report"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

const latestProtocol = "2025-06-18"

var supportedProtocols = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server is an MCP stdio server.
type Server struct {
	Version string
	out     io.Writer
	mu      sync.Mutex
}

// Serve reads newline-delimited JSON-RPC messages from in until EOF.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.send(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // notification (e.g. notifications/initialized)
		}
		result, rerr := s.handle(ctx, req)
		resp := response{JSONRPC: "2.0", ID: req.ID}
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = result
		}
		s.send(resp)
	}
	return sc.Err()
}

func (s *Server) send(r response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(r)
	fmt.Fprintf(s.out, "%s\n", b)
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := latestProtocol
		if supportedProtocols[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "k8s-guardian", "version": s.Version},
			"instructions": "Use validate_manifest on every Kubernetes manifest or Helm output you write or edit, " +
				"and fix_manifest to remediate the findings before presenting YAML to the user.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		text, isErr := s.callTool(ctx, p.Name, p.Arguments)
		return map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
			"isError": isErr,
		}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func schema(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

var baseTools = []map[string]any{
	{
		"name":        "validate_manifest",
		"description": "Validate Kubernetes YAML (one or more documents) against k8s-guardian guardrails: resource requests/limits, non-root, privilege escalation, capabilities, probes, pinned image tags, host namespaces, seccomp and more. Returns a list of findings.",
		"inputSchema": schema(map[string]any{
			"yaml": map[string]string{"type": "string", "description": "Kubernetes manifest YAML"},
			"skip": map[string]string{"type": "string", "description": "Optional comma separated rule IDs/names to skip"},
		}, "yaml"),
	},
	{
		"name":        "fix_manifest",
		"description": "Auto-fix Kubernetes YAML. Applies deterministic fixes (resources, securityContext, seccomp, host namespaces) preserving comments; with use_ai=true Claude also fixes the rest (probes, image tags). Returns the fixed YAML and remaining findings.",
		"inputSchema": schema(map[string]any{
			"yaml":   map[string]string{"type": "string", "description": "Kubernetes manifest YAML"},
			"use_ai": map[string]string{"type": "boolean", "description": "Also use Claude to fix findings without a deterministic fix (requires ANTHROPIC_API_KEY for the server)"},
			"skip":   map[string]string{"type": "string", "description": "Optional comma separated rule IDs/names to skip"},
		}, "yaml"),
	},
	{
		"name":        "audit_cluster_resource",
		"description": "Fetch a live resource from the current kubectl context (e.g. 'deployment/my-app' or 'deployments') and validate it.",
		"inputSchema": schema(map[string]any{
			"resource":  map[string]string{"type": "string", "description": "kubectl resource, e.g. deployment/my-app"},
			"namespace": map[string]string{"type": "string", "description": "Namespace (optional)"},
		}, "resource"),
	},
	{
		"name":        "list_rules",
		"description": "List all k8s-guardian rules with IDs, severities and descriptions.",
		"inputSchema": schema(map[string]any{}),
	},
}

func (s *Server) callBaseTool(ctx context.Context, name string, args toolArgs) (string, bool) {
	opts, err := loadOptions(args.Skip)
	if err != nil {
		return err.Error(), true
	}
	switch name {
	case "validate_manifest":
		f, err := manifest.Parse([]byte(args.YAML), "input.yaml")
		if err != nil {
			return err.Error(), true
		}
		return report.Text(rules.Validate(f.Objects, opts)), false
	case "fix_manifest":
		f, err := manifest.Parse([]byte(args.YAML), "input.yaml")
		if err != nil {
			return err.Error(), true
		}
		res, err := guardian.Fix(ctx, f, guardian.FixOptions{Rules: opts, AI: args.UseAI, AIMinSeverity: rules.Warning})
		if err != nil {
			return err.Error(), true
		}
		out, err := res.File.Encode()
		if err != nil {
			return err.Error(), true
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Applied %d deterministic fix(es)", res.Fixed)
		if res.AIApplied {
			b.WriteString(" plus Claude AI fixes")
		}
		b.WriteString(".\n\nFixed manifest:\n```yaml\n")
		b.Write(out)
		b.WriteString("```\n")
		if res.AINotes != "" {
			b.WriteString("\nAI notes:\n" + res.AINotes + "\n")
		}
		b.WriteString("\nRemaining findings:\n" + report.Text(res.Remaining))
		return b.String(), false
	case "audit_cluster_resource":
		if args.Resource == "" {
			return "resource is required", true
		}
		f, err := kube.Get(strings.Fields(args.Resource), args.Namespace, "", false)
		if err != nil {
			return err.Error(), true
		}
		return report.Text(rules.Validate(f.Objects, opts)), false
	}
	return "unknown tool: " + name, true
}

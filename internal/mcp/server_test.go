package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServer(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"validate_manifest","arguments":{"yaml":"kind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - name: c\n    image: nginx\n"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"nope"}`,
	}, "\n")
	var out bytes.Buffer
	if err := (&Server{Version: "test"}).Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 responses, got %d: %s", len(lines), out.String())
	}
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(lines[0]), &init)
	if init.Result.ProtocolVersion != "2025-03-26" {
		t.Errorf("protocol negotiation: %s", lines[0])
	}
	if !strings.Contains(lines[1], "fix_manifest") {
		t.Errorf("tools/list: %s", lines[1])
	}
	if !strings.Contains(lines[2], "KG010") || !strings.Contains(lines[2], `"isError":false`) {
		t.Errorf("validate_manifest: %s", lines[2])
	}
	if !strings.Contains(lines[3], "-32601") {
		t.Errorf("unknown method: %s", lines[3])
	}
}

func TestAuditToolRejectsFlagInjection(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"audit_cluster_resource","arguments":{"resource":"deployments --server=https://attacker.example"}}}`
	var out bytes.Buffer
	(&Server{Version: "test"}).Serve(context.Background(), strings.NewReader(in), &out)
	if !strings.Contains(out.String(), `"isError":true`) || !strings.Contains(out.String(), `invalid resource \"--server=https://attacker.example\"`) {
		t.Errorf("unexpected response: %s", out.String())
	}
}

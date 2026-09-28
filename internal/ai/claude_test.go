package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/rules"
)

func sse(w io.Writer, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func TestFixAgainstMockAPI(t *testing.T) {
	var body map[string]any
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		answer := "```yaml\napiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n```\n- added probes"
		sse(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{},
			"stop_reason": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}})
		sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		sse(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": answer}})
		sse(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		sse(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]int{"output_tokens": 10}})
		sse(w, "message_stop", map[string]any{"type": "message_stop"})
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test")

	fixed, notes, err := New("").Fix(context.Background(), "kind: Pod\n", []rules.Finding{{RuleID: "KG008", Rule: "liveness-probe", Message: "missing livenessProbe"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fixed, "kind: Pod") || strings.Contains(fixed, "```") {
		t.Errorf("unexpected fixed YAML: %q", fixed)
	}
	if notes != "- added probes" {
		t.Errorf("unexpected notes: %q", notes)
	}
	if body["model"] != DefaultModel || body["fallbacks"] != "default" {
		t.Errorf("unexpected request body: model=%v fallbacks=%v", body["model"], body["fallbacks"])
	}
	if !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Errorf("missing fallback beta header, got %q", beta)
	}
}

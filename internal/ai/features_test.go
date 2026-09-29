package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockAPI serves the given answers in order and records request bodies.
func mockAPI(t *testing.T, answers ...string) *[]map[string]any {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		answer := answers[min(len(bodies)-1, len(answers)-1)]
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg", "type": "message", "role": "assistant", "model": DefaultModel, "content": []any{},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}})
		sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		sse(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": answer}})
		sse(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		sse(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]int{"output_tokens": 10}})
		sse(w, "message_stop", map[string]any{"type": "message_stop"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	return &bodies
}

func TestGenerateRulesRetriesInvalidRules(t *testing.T) {
	bad := `{"rules":[{"name":"no-latest","id":"ORG001","severity":"error","description":"d","message":"","scope":"container","kinds":[],"when":[],"assert":[{"path":"image","op":"notMatches","value":"(:latest","values":[]}]}]}`
	good := `{"rules":[
	 {"name":"no-latest","id":"ORG001","severity":"error","description":"No :latest","message":"pin the image","scope":"container","kinds":[],"when":[],"assert":[{"path":"image","op":"notMatches","value":":latest$","values":[]}]},
	 {"name":"require-contact","id":"ORG002","severity":"warning","description":"Contact","message":"","scope":"resource","kinds":["Deployment"],"when":[],"assert":[{"path":"metadata.annotations['example.com/contact']","op":"matches","value":"@","values":[]}]}]}`
	bodies := mockAPI(t, bad, good)
	docs, err := New("").GenerateRules(context.Background(), "forbid latest and require contact email", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[1].Spec.Match.Scope != "resource" {
		t.Fatalf("unexpected docs: %+v", docs)
	}
	if len(*bodies) != 2 {
		t.Fatalf("expected a retry, got %d requests", len(*bodies))
	}
	second, _ := json.Marshal((*bodies)[1]["messages"])
	if !strings.Contains(string(second), "failed validation") || !strings.Contains(string(second), "invalid regex") {
		t.Errorf("retry should explain the validation error: %s", second)
	}
	format, _ := json.Marshal((*bodies)[0]["output_config"])
	if !strings.Contains(string(format), "json_schema") {
		t.Errorf("expected structured output format, got %s", format)
	}
}

func TestRightSize(t *testing.T) {
	mockAPI(t, `{"recommendations":[{"resource":"Deployment/api","container":"api","workload_type":"Go HTTP API","cpu_request":"250m","memory_request":"256Mi","memory_limit":"512Mi","reason":"static Go binary"}]}`)
	recs, err := New("").RightSize(context.Background(), []WorkloadInput{{Resource: "Deployment/api", Kind: "Deployment", Replicas: "2",
		Containers: []ContainerInput{{Name: "api", Image: "ghcr.io/x/api:1", Resources: map[string]string{"requests.cpu": "4"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].CPURequest != "250m" {
		t.Fatalf("unexpected: %+v", recs)
	}
}

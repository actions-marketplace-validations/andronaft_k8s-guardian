package guardian

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

// fakeClaude answers every request with the given YAML in a fenced block.
func fakeClaude(t *testing.T, answer string) *int {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(name string, v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "x", "content": []any{}, "usage": map[string]int{"input_tokens": 1}}})
		ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "```yaml\n" + answer + "```\n- done"}})
		ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		ev("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]int{"output_tokens": 1}})
		ev("message_stop", map[string]any{"type": "message_stop"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	return &calls
}

const twoObjects = `apiVersion: v1
kind: Pod
metadata: {name: a}
spec:
  containers: [{name: c, image: nginx:1.27}]
---
apiVersion: v1
kind: Service
metadata: {name: svc}
spec: {ports: [{port: 80}]}
`

func TestAIFixMustKeepTheSameResources(t *testing.T) {
	// The "fixed" answer silently drops the Service.
	fakeClaude(t, "apiVersion: v1\nkind: Pod\nmetadata: {name: a}\nspec:\n  containers: [{name: c, image: nginx:1.27}]\n")
	f, _ := manifest.Parse([]byte(twoObjects), "app.yaml")
	_, err := Fix(context.Background(), f, FixOptions{Rules: rules.NewOptions(""), AI: true, AIMinSeverity: rules.Info})
	if err == nil || !strings.Contains(err.Error(), "changed the set of resources") {
		t.Fatalf("expected the dropped Service to be rejected, got %v", err)
	}
}

func TestAIFixNeverSendsSecrets(t *testing.T) {
	calls := fakeClaude(t, "")
	f, _ := manifest.Parse([]byte(twoObjects+"---\napiVersion: v1\nkind: Secret\nmetadata: {name: db}\nstringData: {password: hunter2}\n"), "app.yaml")
	_, err := Fix(context.Background(), f, FixOptions{Rules: rules.NewOptions(""), AI: true, AIMinSeverity: rules.Info})
	if err == nil || !strings.Contains(err.Error(), "contains a Secret") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if *calls != 0 {
		t.Fatalf("the Claude API was called %d time(s)", *calls)
	}
}

func TestAIFixCannotIntroduceNewErrors(t *testing.T) {
	// A prompt-injected answer that "fixes" the Pod by making it privileged.
	fakeClaude(t, "apiVersion: v1\nkind: Pod\nmetadata: {name: a}\nspec:\n  hostNetwork: true\n  containers: [{name: c, image: nginx:latest}]\n---\napiVersion: v1\nkind: Service\nmetadata: {name: svc}\nspec: {ports: [{port: 80}]}\n")
	f, _ := manifest.Parse([]byte(twoObjects), "app.yaml")
	_, err := Fix(context.Background(), f, FixOptions{Rules: rules.NewOptions(""), AI: true, AIMinSeverity: rules.Info})
	// hostNetwork (KG011) is removed again by the deterministic fix, but the
	// mutable :latest tag (KG010) can't be auto-fixed and must be rejected.
	if err == nil || !strings.Contains(err.Error(), "introduced a new problem (KG010") {
		t.Fatalf("expected the injected :latest image to be rejected, got %v", err)
	}
}

package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// WorkloadInput is what Claude sees about a workload when right-sizing. It
// deliberately excludes env values, annotations and secrets.
type WorkloadInput struct {
	Resource   string           `json:"resource"`
	Kind       string           `json:"kind"`
	Replicas   string           `json:"replicas"`
	Containers []ContainerInput `json:"containers"`
}

type ContainerInput struct {
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Ports     []string          `json:"ports,omitempty"`
	EnvNames  []string          `json:"envNames,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Resources map[string]string `json:"resources"`
	// ObservedUsage is the current average usage from metrics-server, when known.
	ObservedUsage map[string]string `json:"observedUsage,omitempty"`
}

// Recommendation is Claude's right-sizing advice for one container.
type Recommendation struct {
	Resource      string `json:"resource"`
	Container     string `json:"container"`
	WorkloadType  string `json:"workload_type"`
	CPURequest    string `json:"cpu_request"`
	MemoryRequest string `json:"memory_request"`
	MemoryLimit   string `json:"memory_limit"`
	Reason        string `json:"reason"`
}

const rightsizePrompt = `You are a Kubernetes capacity-planning expert helping teams cut cloud spend without hurting reliability.
For every container you receive (image, ports, env var names, command, current requests/limits, replicas), infer the workload type
from the image and context (for example Go/Rust static binaries, JVM/Spring, Node.js, Python/Gunicorn, nginx/envoy proxies, Redis, Postgres, batch jobs)
and recommend steady-state resource requests and a memory limit.

Guidelines:
- Be realistic, not stingy: JVM services need heap + metaspace headroom, databases and caches need memory for their working set.
- Typical stateless Go/Rust HTTP services: 50m-500m CPU, 64Mi-512Mi memory. Node.js/Python: 100m-500m, 128Mi-512Mi. JVM: 250m-1, 512Mi-2Gi.
- If observedUsage is present, it is a point-in-time average from metrics-server: base the recommendation on it, with headroom
  for peaks (CPU ~2x, memory ~1.5x and never below the working set of the runtime, e.g. JVM heap). Mention it in the reason.
- If the current values already look reasonable, repeat them unchanged and say so.
- memory_limit must be >= memory_request. Use Kubernetes quantity syntax (500m, 1, 256Mi, 1Gi).
- reason: one or two sentences, mention the inferred workload type and why the values fit.
Return one recommendation per container.`

func rightsizeSchema() map[string]any {
	rec := object(map[string]any{
		"resource":       map[string]any{"type": "string"},
		"container":      map[string]any{"type": "string"},
		"workload_type":  map[string]any{"type": "string"},
		"cpu_request":    map[string]any{"type": "string"},
		"memory_request": map[string]any{"type": "string"},
		"memory_limit":   map[string]any{"type": "string"},
		"reason":         map[string]any{"type": "string"},
	})
	return object(map[string]any{"recommendations": map[string]any{"type": "array", "items": rec}})
}

// RightSize asks Claude for resource recommendations.
func (c *Client) RightSize(ctx context.Context, workloads []WorkloadInput) ([]Recommendation, error) {
	in, err := json.MarshalIndent(workloads, "", "  ")
	if err != nil {
		return nil, err
	}
	out, err := c.request(ctx, rightsizePrompt, []anthropic.MessageParam{userText("Workloads:\n" + string(in))}, rightsizeSchema())
	if err != nil {
		return nil, err
	}
	var resp struct {
		Recommendations []Recommendation `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("invalid recommendation JSON: %w", err)
	}
	return resp.Recommendations, nil
}

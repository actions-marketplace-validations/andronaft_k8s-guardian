// Package cost estimates the monthly cloud cost of workloads from their
// resource requests and (optionally) asks Claude to right-size them.
package cost

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/andronaft/k8s-guardian/internal/ai"
	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/quantity"
	"github.com/andronaft/k8s-guardian/internal/rules"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// HoursPerMonth is the average number of hours in a month.
const HoursPerMonth = 730

// Pricing is the price of reserved node capacity.
type Pricing struct {
	CPUHour float64 `json:"cpuHour"` // $ per vCPU-hour
	GiBHour float64 `json:"gibHour"` // $ per GiB-hour
}

// Default prices approximate on-demand general-purpose instances of the big
// cloud providers split into a per-vCPU and per-GiB price. They are
// deliberately simple; override them for your contract/region.
const (
	DefaultCPUHour = 0.0316
	DefaultGiBHour = 0.0042
)

// DefaultPricing honours K8S_GUARDIAN_CPU_HOUR and K8S_GUARDIAN_GIB_HOUR.
func DefaultPricing() Pricing {
	p := Pricing{CPUHour: DefaultCPUHour, GiBHour: DefaultGiBHour}
	if v, err := strconv.ParseFloat(os.Getenv("K8S_GUARDIAN_CPU_HOUR"), 64); err == nil && v > 0 {
		p.CPUHour = v
	}
	if v, err := strconv.ParseFloat(os.Getenv("K8S_GUARDIAN_GIB_HOUR"), 64); err == nil && v > 0 {
		p.GiBHour = v
	}
	return p
}

// Monthly returns the monthly price of the given capacity.
func (p Pricing) Monthly(milliCPU, bytes int64) float64 {
	return (float64(milliCPU)/1000*p.CPUHour + float64(bytes)/(1<<30)*p.GiBHour) * HoursPerMonth
}

// Container holds the requests of a single container.
type Container struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Init     bool   `json:"init,omitempty"`
	MilliCPU int64  `json:"milliCpu"`
	Memory   int64  `json:"memoryBytes"`
	Missing  bool   `json:"missingRequests,omitempty"`

	// Observed usage (cost --usage), averaged over the running pods.
	UsedCPU    int64 `json:"usedMilliCpu,omitempty"`
	UsedMemory int64 `json:"usedMemoryBytes,omitempty"`
	HasUsage   bool  `json:"hasUsage,omitempty"`
	// Offline suggestion derived from usage (with headroom).
	SuggestedCPU    int64   `json:"suggestedMilliCpu,omitempty"`
	SuggestedMemory int64   `json:"suggestedMemoryBytes,omitempty"`
	UsageSavings    float64 `json:"usageMonthlySavings,omitempty"`

	node *rules.Container
}

// Workload is the cost estimate of one resource.
type Workload struct {
	Resource    string      `json:"resource"`
	Source      string      `json:"source"`
	Namespace   string      `json:"namespace,omitempty"`
	Kind        string      `json:"kind"`
	MinReplicas int         `json:"minReplicas"`
	MaxReplicas int         `json:"maxReplicas"`
	PodCPU      int64       `json:"podMilliCpu"`
	PodMemory   int64       `json:"podMemoryBytes"`
	MonthlyMin  float64     `json:"monthlyMin"`
	MonthlyMax  float64     `json:"monthlyMax"`
	Containers  []Container `json:"containers"`
	Notes       []string    `json:"notes,omitempty"`

	target *rules.Target
}

// Key identifies the workload uniquely: "namespace/Kind/name" (or
// "Kind/name" without a namespace).
func (w *Workload) Key() string {
	if w.Namespace == "" {
		return w.Resource
	}
	return w.Namespace + "/" + w.Resource
}

// Estimate computes costs for all workloads in objs. HorizontalPodAutoscalers
// in the same set turn the replica count into a min..max range.
func Estimate(objs []*manifest.Object, p Pricing) []*Workload {
	hpas := map[string][2]int{}
	for _, o := range objs {
		if o.Kind() != "HorizontalPodAutoscaler" {
			continue
		}
		ref := yamlx.String(o.Root, "spec", "scaleTargetRef", "kind") + "/" + yamlx.String(o.Root, "spec", "scaleTargetRef", "name")
		min := atoi(yamlx.String(o.Root, "spec", "minReplicas"), 1)
		max := atoi(yamlx.String(o.Root, "spec", "maxReplicas"), min)
		hpas[o.Namespace()+"|"+ref] = [2]int{min, max}
	}
	var out []*Workload
	for _, o := range objs {
		t := rules.NewTarget(o)
		if t == nil {
			continue
		}
		w := &Workload{Resource: o.Ref(), Source: o.Source, Namespace: o.Namespace(), Kind: o.Kind(), target: t}
		var sumCPU, sumMem, initCPU, initMem int64
		for _, c := range t.Containers {
			cc := Container{Name: c.Name, Image: yamlx.String(c.Node, "image"), Init: c.Init, node: c}
			cpu := yamlx.String(c.Node, "resources", "requests", "cpu")
			mem := yamlx.String(c.Node, "resources", "requests", "memory")
			// Kubernetes defaults requests to limits when only limits are set.
			if cpu == "" {
				cpu = yamlx.String(c.Node, "resources", "limits", "cpu")
			}
			if mem == "" {
				mem = yamlx.String(c.Node, "resources", "limits", "memory")
			}
			cc.MilliCPU, _ = quantity.MilliCPU(orZero(cpu))
			cc.Memory, _ = quantity.Bytes(orZero(mem))
			cc.Missing = cpu == "" || mem == ""
			if cc.Missing {
				w.Notes = append(w.Notes, fmt.Sprintf("container %q has no CPU/memory requests: real cost is unknown (BestEffort pods can be evicted first)", c.Name))
			}
			if cc.MilliCPU >= 2000 {
				w.Notes = append(w.Notes, fmt.Sprintf("container %q requests %s CPU: make sure it really uses it (run `cost --ai` for advice)", c.Name, quantity.FormatCPU(cc.MilliCPU)))
			}
			if cc.Memory >= 4<<30 {
				w.Notes = append(w.Notes, fmt.Sprintf("container %q requests %s memory: make sure it really uses it", c.Name, quantity.FormatBytes(cc.Memory)))
			}
			if c.Init {
				initCPU, initMem = max(initCPU, cc.MilliCPU), max(initMem, cc.Memory)
			} else {
				sumCPU += cc.MilliCPU
				sumMem += cc.Memory
			}
			w.Containers = append(w.Containers, cc)
		}
		w.PodCPU, w.PodMemory = max(sumCPU, initCPU), max(sumMem, initMem)

		switch o.Kind() {
		case "Deployment", "StatefulSet", "ReplicaSet", "ReplicationController", "Rollout":
			n := atoi(yamlx.String(o.Root, "spec", "replicas"), 1)
			w.MinReplicas, w.MaxReplicas = n, n
			if r, ok := hpas[o.Namespace()+"|"+o.Ref()]; ok {
				w.MinReplicas, w.MaxReplicas = r[0], r[1]
				w.Notes = append(w.Notes, fmt.Sprintf("scaled by an HPA between %d and %d replicas", r[0], r[1]))
			}
		case "DaemonSet":
			w.MinReplicas, w.MaxReplicas = 1, 1
			w.Notes = append(w.Notes, "DaemonSet: cost shown per node")
		case "Job", "CronJob":
			w.MinReplicas, w.MaxReplicas = 1, 1
			w.Notes = append(w.Notes, "batch workload: cost shown as if it ran all month; real cost is proportional to run time")
		default:
			w.MinReplicas, w.MaxReplicas = 1, 1
		}
		pod := p.Monthly(w.PodCPU, w.PodMemory)
		w.MonthlyMin, w.MonthlyMax = pod*float64(w.MinReplicas), pod*float64(w.MaxReplicas)
		out = append(out, w)
	}
	return out
}

// Advice is a Claude recommendation with its estimated monthly savings.
type Advice struct {
	ai.Recommendation
	Current Container `json:"current"`
	Savings float64   `json:"monthlySavings"` // negative = costs more
}

// RightSize asks Claude for per-container recommendations and prices them
// using the minimum replica count.
func RightSize(ctx context.Context, client *ai.Client, ws []*Workload, p Pricing) ([]Advice, error) {
	var in []ai.WorkloadInput
	for _, w := range ws {
		wi := ai.WorkloadInput{Resource: w.Key(), Kind: w.Kind, Replicas: strconv.Itoa(w.MinReplicas)}
		if w.MinReplicas != w.MaxReplicas {
			wi.Replicas = fmt.Sprintf("%d-%d (HPA)", w.MinReplicas, w.MaxReplicas)
		}
		for _, c := range w.Containers {
			n := c.node.Node
			ci := ai.ContainerInput{Name: c.Name, Image: c.Image, Resources: map[string]string{}}
			for _, port := range yamlx.Items(yamlx.Get(n, "ports")) {
				ci.Ports = append(ci.Ports, yamlx.String(port, "containerPort")+"/"+yamlx.String(port, "name"))
			}
			for _, e := range yamlx.Items(yamlx.Get(n, "env")) {
				ci.EnvNames = append(ci.EnvNames, yamlx.String(e, "name"))
			}
			for _, key := range []string{"command", "args"} {
				for _, a := range yamlx.Items(yamlx.Get(n, key)) {
					ci.Command = append(ci.Command, a.Value)
				}
			}
			for _, kind := range []string{"requests", "limits"} {
				for _, r := range []string{"cpu", "memory"} {
					if v := yamlx.String(n, "resources", kind, r); v != "" {
						ci.Resources[kind+"."+r] = v
					}
				}
			}
			if c.HasUsage {
				ci.ObservedUsage = map[string]string{"cpu": quantity.FormatCPU(c.UsedCPU), "memory": quantity.FormatBytes(c.UsedMemory)}
			}
			wi.Containers = append(wi.Containers, ci)
		}
		in = append(in, wi)
	}
	recs, err := client.RightSize(ctx, in)
	if err != nil {
		return nil, err
	}
	var out []Advice
	for _, r := range recs {
		w, c := find(ws, r.Resource, r.Container)
		if c == nil {
			continue
		}
		cpu, err1 := quantity.MilliCPU(r.CPURequest)
		mem, err2 := quantity.Bytes(r.MemoryRequest)
		if err1 != nil || err2 != nil {
			continue
		}
		saving := (p.Monthly(c.MilliCPU, c.Memory) - p.Monthly(cpu, mem)) * float64(w.MinReplicas)
		if c.Init {
			saving = 0 // init containers rarely drive the pod's effective request
		}
		out = append(out, Advice{Recommendation: r, Current: *c, Savings: saving})
	}
	return out, nil
}

// Apply writes the recommended requests/limits into the manifests.
func Apply(ws []*Workload, advice []Advice) int {
	n := 0
	for _, a := range advice {
		_, c := find(ws, a.Resource, a.Container)
		if c == nil {
			continue
		}
		res := yamlx.EnsureMap(c.node.Node, "resources")
		req := yamlx.EnsureMap(res, "requests")
		yamlx.Set(req, "cpu", yamlx.Str(a.CPURequest))
		yamlx.Set(req, "memory", yamlx.Str(a.MemoryRequest))
		if a.MemoryLimit != "" {
			yamlx.Set(yamlx.EnsureMap(res, "limits"), "memory", yamlx.Str(a.MemoryLimit))
		}
		n++
	}
	return n
}

func find(ws []*Workload, resource, container string) (*Workload, *Container) {
	for _, w := range ws {
		if w.Key() != resource {
			continue
		}
		for i := range w.Containers {
			if w.Containers[i].Name == container {
				return w, &w.Containers[i]
			}
		}
	}
	return nil, nil
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// UsageSource provides observed usage per container of a workload.
type UsageSource interface {
	// Usage returns per-container usage and the number of pods it is based on.
	Usage(w *Workload, namespace string) (map[string]cluster.Usage, int, error)
	// Headroom returns the CPU and memory multipliers applied to the usage.
	Headroom() (cpu, memory float64)
	// Describe explains the measurement for a workload note.
	Describe(pods int) string
}

const (
	minCPU    = 10       // millicores
	minMemory = 32 << 20 // bytes
)

// MetricsServer reads a point-in-time snapshot with `kubectl top`.
type MetricsServer struct{ Cluster cluster.Cluster }

func (m MetricsServer) Usage(w *Workload, ns string) (map[string]cluster.Usage, int, error) {
	sel := selector(w)
	if len(sel) == 0 {
		return nil, 0, fmt.Errorf("no selector/labels to find its pods")
	}
	u, n, err := m.Cluster.PodUsage(ns, sel)
	if err != nil {
		return nil, 0, fmt.Errorf("is metrics-server installed? %w", err)
	}
	return u, n, nil
}

// Snapshots can miss peaks: CPU gets 2x (it is throttled, not killed), memory 1.5x.
func (MetricsServer) Headroom() (float64, float64) { return 2.0, 1.5 }

func (MetricsServer) Describe(pods int) string {
	return fmt.Sprintf("usage sampled from %d running pod(s) (point-in-time snapshot, check peak load before cutting requests)", pods)
}

// AttachUsage reads observed usage for every workload from src and derives
// right-sizing suggestions. It returns notes about workloads it could not
// measure.
func AttachUsage(ws []*Workload, src UsageSource, defaultNS string, p Pricing) []string {
	var notes []string
	cpuHead, memHead := src.Headroom()
	for _, w := range ws {
		if w.Kind == "Job" || w.Kind == "CronJob" {
			continue
		}
		ns := w.Namespace
		if ns == "" {
			ns = defaultNS
		}
		usage, pods, err := src.Usage(w, ns)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: could not read usage: %v", w.Resource, err))
			continue
		}
		if pods == 0 {
			notes = append(notes, w.Resource+": no pods found, usage unknown")
			continue
		}
		for i := range w.Containers {
			ct := &w.Containers[i]
			u, ok := usage[ct.Name]
			if !ok || ct.Init {
				continue
			}
			ct.UsedCPU, _ = quantity.MilliCPU(u.CPU)
			ct.UsedMemory, _ = quantity.Bytes(u.Memory)
			ct.HasUsage = true
			ct.SuggestedCPU = roundUp(max(int64(float64(ct.UsedCPU)*cpuHead), minCPU), 5)
			ct.SuggestedMemory = roundUp(max(int64(float64(ct.UsedMemory)*memHead), minMemory), 16<<20)
			ct.UsageSavings = (p.Monthly(ct.MilliCPU, ct.Memory) - p.Monthly(ct.SuggestedCPU, ct.SuggestedMemory)) * float64(w.MinReplicas)
		}
		w.Notes = append(w.Notes, src.Describe(pods))
	}
	return notes
}

func selector(w *Workload) map[string]string {
	root := w.target.Obj.Root
	n := yamlx.Path(root, "spec", "selector", "matchLabels")
	if w.Kind == "Pod" {
		n = yamlx.Path(root, "metadata", "labels")
	}
	out := map[string]string{}
	if n != nil {
		for i := 0; i+1 < len(n.Content); i += 2 {
			out[n.Content[i].Value] = n.Content[i+1].Value
		}
	}
	return out
}

func roundUp(v, step int64) int64 {
	return (v + step - 1) / step * step
}

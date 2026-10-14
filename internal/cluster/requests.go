package cluster

import (
	"strings"

	"github.com/andronaft/k8s-guardian/internal/quantity"
)

// podRequests is a jsonpath that prints one line per scheduled pod:
// node|cpu,memory;...|cpu,memory,restartPolicy;...|cpu,memory
// (containers, init containers, overhead).
const podRequests = `{range .items[*]}{.spec.nodeName}{"|"}` +
	`{range .spec.containers[*]}{.resources.requests.cpu}{","}{.resources.requests.memory}{";"}{end}{"|"}` +
	`{range .spec.initContainers[*]}{.resources.requests.cpu}{","}{.resources.requests.memory}{","}{.restartPolicy}{";"}{end}{"|"}` +
	`{.spec.overhead.cpu}{","}{.spec.overhead.memory}{"\n"}{end}`

// NodeRequests runs `kubectl get pods -A` for the pods that hold resources
// (not finished, already scheduled) and sums their requests per node.
func (k *Kubectl) NodeRequests() (map[string]Requests, error) {
	return cached(k, "node-requests", func() (map[string]Requests, error) {
		out, err := k.run("get", "pods", "--all-namespaces",
			"--field-selector=status.phase!=Succeeded,status.phase!=Failed",
			"-o", "jsonpath="+podRequests)
		if err != nil {
			return nil, err
		}
		return ParseNodeRequests(string(out)), nil
	})
}

// ParseNodeRequests sums the output of the podRequests jsonpath per node,
// with the scheduler's formula for a pod's requests: the containers plus
// sidecars (init containers with restartPolicy Always), or the largest
// init container with the sidecars started before it if that is more,
// plus the pod overhead.
func ParseNodeRequests(out string) map[string]Requests {
	nodes := map[string]Requests{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) != 4 || f[0] == "" {
			continue // malformed, or not scheduled yet
		}
		var app, sidecars, init Requests
		for _, c := range items(f[1]) {
			app = app.add(parseRequests(c))
		}
		for _, c := range items(f[2]) {
			r := parseRequests(c)
			if len(c) > 2 && c[2] == "Always" {
				sidecars = sidecars.add(r)
				continue
			}
			init = init.max(r.add(sidecars))
		}
		pod := app.add(sidecars).max(init).add(parseRequests(strings.Split(f[3], ",")))
		nodes[f[0]] = nodes[f[0]].add(pod)
	}
	return nodes
}

func items(s string) [][]string {
	var out [][]string
	for _, it := range strings.Split(s, ";") {
		if it != "" {
			out = append(out, strings.Split(it, ","))
		}
	}
	return out
}

func parseRequests(f []string) Requests {
	var r Requests
	if len(f) > 0 && f[0] != "" {
		r.MilliCPU, _ = quantity.MilliCPU(f[0])
	}
	if len(f) > 1 && f[1] != "" {
		r.Bytes, _ = quantity.Bytes(f[1])
	}
	return r
}

func (r Requests) add(o Requests) Requests {
	return Requests{r.MilliCPU + o.MilliCPU, r.Bytes + o.Bytes}
}

func (r Requests) max(o Requests) Requests {
	return Requests{max(r.MilliCPU, o.MilliCPU), max(r.Bytes, o.Bytes)}
}

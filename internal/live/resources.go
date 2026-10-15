package live

import (
	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/quantity"
	"github.com/andronaft/k8s-guardian/internal/rules"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

const (
	cpuKey = "cpu"
	memKey = "memory"
)

type ctrRes struct {
	name     string
	init     bool
	req, lim map[string]int64 // only resources that are effectively set
}

// podRes holds the effective pod-level requests/limits after Kubernetes
// defaulting (limits -> requests) and LimitRange defaults, with sidecars
// counted like the scheduler and ResourceQuota do.
type podRes struct {
	req, lim   map[string]int64
	missingReq map[string][]string // resource -> containers without a request
	missingLim map[string][]string
	containers []ctrRes
}

func podResources(t *rules.Target, lrs []cluster.LimitRangeItem) podRes {
	defReq, defLim := map[string]string{}, map[string]string{}
	for _, lr := range lrs {
		if lr.Type != "Container" {
			continue
		}
		for k, v := range lr.DefaultRequest {
			defReq[k] = v
		}
		for k, v := range lr.Default {
			defLim[k] = v
		}
	}
	pr := podRes{req: map[string]int64{}, lim: map[string]int64{}, missingReq: map[string][]string{}, missingLim: map[string][]string{}}
	// The scheduler's formula: app containers plus sidecars, or the largest
	// init container plus the sidecars started before it, if that is more.
	initReq, initLim := map[string]int64{}, map[string]int64{}
	sideReq, sideLim := map[string]int64{}, map[string]int64{}
	for _, c := range t.Containers {
		cr := ctrRes{name: c.Name, init: c.Init, req: map[string]int64{}, lim: map[string]int64{}}
		for _, key := range []string{cpuKey, memKey} {
			lim := yamlx.String(c.Node, "resources", "limits", key)
			if lim == "" {
				lim = defLim[key]
			}
			req := yamlx.String(c.Node, "resources", "requests", key)
			if req == "" {
				req = yamlx.String(c.Node, "resources", "limits", key) // API server defaulting
			}
			if req == "" {
				req = defReq[key]
			}
			if req == "" {
				req = lim // LimitRange default limit also becomes the request
			}
			if v, ok := parse(key, req); ok {
				cr.req[key] = v
			} else {
				pr.missingReq[key] = append(pr.missingReq[key], c.Name)
			}
			if v, ok := parse(key, lim); ok {
				cr.lim[key] = v
			} else {
				pr.missingLim[key] = append(pr.missingLim[key], c.Name)
			}
			switch {
			case c.Sidecar():
				sideReq[key] += cr.req[key]
				sideLim[key] += cr.lim[key]
				pr.req[key] += cr.req[key]
				pr.lim[key] += cr.lim[key]
			case c.Init:
				initReq[key] = max(initReq[key], cr.req[key]+sideReq[key])
				initLim[key] = max(initLim[key], cr.lim[key]+sideLim[key])
			default:
				pr.req[key] += cr.req[key]
				pr.lim[key] += cr.lim[key]
			}
		}
		pr.containers = append(pr.containers, cr)
	}
	for _, key := range []string{cpuKey, memKey} {
		pr.req[key] = max(pr.req[key], initReq[key])
		pr.lim[key] = max(pr.lim[key], initLim[key])
	}
	return pr
}

// parse converts a quantity for the given resource (cpu -> millicores,
// memory -> bytes, pods -> count).
func parse(res, v string) (int64, bool) {
	if v == "" {
		return 0, false
	}
	var n int64
	var err error
	switch res {
	case cpuKey:
		n, err = quantity.MilliCPU(v)
	default:
		n, err = quantity.Bytes(v)
	}
	return n, err == nil
}

func format(res string, v int64) string {
	switch res {
	case cpuKey:
		return quantity.FormatCPU(v)
	case memKey:
		return quantity.FormatBytes(v)
	}
	return quantity.FormatCPU(v * 1000)
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

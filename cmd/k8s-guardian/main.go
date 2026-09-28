// Command k8s-guardian validates and auto-fixes Kubernetes manifests. The same
// binary works as a kubectl plugin when installed (or symlinked) as
// kubectl-guard, and as an MCP server via `k8s-guardian mcp`.
package main

import (
	"os"

	"github.com/andronaft/k8s-guardian/internal/cli"
)

var version = "dev"

func main() {
	app := &cli.App{
		Name:    cli.ProgramName(os.Args[0]),
		Version: version,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	os.Exit(app.Run(os.Args[1:]))
}

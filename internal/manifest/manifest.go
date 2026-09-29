// Package manifest loads multi-document Kubernetes YAML into editable node
// trees and writes them back while preserving comments and key order.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// Object is a single Kubernetes resource inside a File.
type Object struct {
	Root   *yaml.Node // mapping node of the resource
	Source string
}

func (o *Object) Kind() string       { return yamlx.String(o.Root, "kind") }
func (o *Object) Name() string       { return yamlx.String(o.Root, "metadata", "name") }
func (o *Object) Namespace() string  { return yamlx.String(o.Root, "metadata", "namespace") }
func (o *Object) APIVersion() string { return yamlx.String(o.Root, "apiVersion") }

// Ref returns a human readable reference such as "Deployment/web".
func (o *Object) Ref() string {
	name := o.Name()
	if name == "" {
		name = "<unnamed>"
	}
	return o.Kind() + "/" + name
}

// Annotation returns metadata.annotations[key].
func (o *Object) Annotation(key string) string {
	return yamlx.String(o.Root, "metadata", "annotations", key)
}

// File is a parsed YAML stream.
type File struct {
	Source   string
	Docs     []*yaml.Node
	Objects  []*Object
	Writable bool // false for rendered Helm charts and cluster resources
}

// Parse decodes a (possibly multi-document) YAML stream.
func Parse(data []byte, source string) (*File, error) {
	f := &File{Source: source, Writable: true}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}
		d := doc
		f.Docs = append(f.Docs, &d)
		f.addObject(d.Content[0])
	}
	return f, nil
}

func (f *File) addObject(root *yaml.Node) {
	o := &Object{Root: root, Source: f.Source}
	if strings.HasSuffix(o.Kind(), "List") {
		if items := yamlx.Get(root, "items"); items != nil && items.Kind == yaml.SequenceNode {
			for _, it := range items.Content {
				if it.Kind == yaml.MappingNode {
					f.addObject(it)
				}
			}
			return
		}
	}
	// Skip empty documents, k8s-guardian's own custom rule files and
	// Kustomize configuration (only meaningful when rendered).
	if o.Kind() == "" || strings.HasPrefix(o.APIVersion(), "k8s-guardian.io/") || strings.HasPrefix(o.APIVersion(), "kustomize.config.k8s.io/") {
		return
	}
	f.Objects = append(f.Objects, o)
}

// Encode renders the file back to YAML.
func (f *File) Encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, d := range f.Docs {
		if err := enc.Encode(d); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Load reads manifests from a file, a directory (recursively), a Helm chart
// directory (rendered with `helm template`) or "-" for stdin.
func Load(path string) ([]*File, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		f, err := Parse(data, "<stdin>")
		if err != nil {
			return nil, err
		}
		return []*File{f}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		f, err := loadFile(path)
		if err != nil {
			return nil, err
		}
		return []*File{f}, nil
	}
	if isChart(path) {
		f, err := renderChart(path)
		if err != nil {
			return nil, err
		}
		return []*File{f}, nil
	}
	if IsKustomization(path) {
		f, err := RenderKustomization(path)
		if err != nil {
			return nil, err
		}
		return []*File{f}, nil
	}
	var files []*File
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != path && (strings.HasPrefix(d.Name(), ".") || isChart(p)) {
				if isChart(p) {
					f, err := renderChart(p)
					if err != nil {
						return err
					}
					files = append(files, f)
				}
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		f, err := loadFile(p)
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	return files, err
}

func loadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, path)
}

func isChart(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "Chart.yaml"))
	return err == nil
}

func renderChart(dir string) (*File, error) {
	if _, err := exec.LookPath("helm"); err != nil {
		return nil, fmt.Errorf("%s is a Helm chart but `helm` was not found in PATH", dir)
	}
	var stderr bytes.Buffer
	cmd := exec.Command("helm", "template", "k8s-guardian", dir)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helm template %s: %v: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	f, err := Parse(out, dir+" (helm template)")
	if err != nil {
		return nil, err
	}
	f.Writable = false
	return f, nil
}

var kustomizationFiles = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// IsKustomization reports whether dir contains a kustomization file.
func IsKustomization(dir string) bool {
	for _, name := range kustomizationFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// RenderKustomization builds a Kustomize directory with `kustomize build`,
// falling back to `kubectl kustomize`. Nested kustomizations inside a walked
// directory are not rendered automatically (bases and overlays would be
// reported twice); pass the overlay directory itself with -f or -k.
func RenderKustomization(dir string) (*File, error) {
	var cmd *exec.Cmd
	switch {
	case lookPath("kustomize"):
		cmd = exec.Command("kustomize", "build", dir)
	case lookPath("kubectl"):
		cmd = exec.Command("kubectl", "kustomize", dir)
	default:
		return nil, fmt.Errorf("%s is a Kustomize directory but neither `kustomize` nor `kubectl` was found in PATH", dir)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %v: %s", cmd.Args[0], strings.Join(cmd.Args[1:], " "), err, strings.TrimSpace(stderr.String()))
	}
	f, err := Parse(out, dir+" (kustomize)")
	if err != nil {
		return nil, err
	}
	f.Writable = false
	return f, nil
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

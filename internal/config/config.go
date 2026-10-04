// Package config loads the optional project configuration file
// (.k8s-guardian.yaml) that check, audit and the GitHub Action share.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Names are looked up in the working directory when --config is not given.
var Names = []string{".k8s-guardian.yaml", ".k8s-guardian.yml"}

// Config is the project configuration. Relative paths are relative to the
// directory of the config file.
type Config struct {
	// Skip lists rule IDs or names to skip, in addition to --skip.
	Skip []string `yaml:"skip"`
	// Severity overrides rule severities: {KG006: error}.
	Severity map[string]string `yaml:"severity"`
	// FailOn is the default for --fail-on.
	FailOn string `yaml:"failOn"`
	// Exclude lists files and directories that directory walks skip.
	// Entries are paths or filepath.Match patterns (no "**").
	Exclude []string `yaml:"exclude"`
	// Rules lists custom rule files or directories.
	Rules []string `yaml:"rules"`
	// Baseline is the default for --baseline.
	Baseline string `yaml:"baseline"`

	// Path is the file the config was read from ("" if none).
	Path string `yaml:"-"`
}

// Load reads path, or the first of Names in the working directory when path
// is "". A missing default file yields an empty Config.
func Load(path string) (*Config, error) {
	explicit := path != ""
	if !explicit {
		for _, n := range Names {
			if _, err := os.Stat(n); err == nil {
				path = n
				break
			}
		}
		if path == "" {
			return &Config{}, nil
		}
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the user's config file
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a typo must not silently disable a setting
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	c.Path = path
	dir := filepath.Dir(path)
	for i, r := range c.Rules {
		c.Rules[i] = resolve(dir, r)
	}
	if c.Baseline != "" {
		c.Baseline = resolve(dir, c.Baseline)
	}
	for i, e := range c.Exclude {
		c.Exclude[i] = resolve(dir, strings.TrimSuffix(e, "/"))
		if _, err := filepath.Match(c.Exclude[i], ""); err != nil {
			return nil, fmt.Errorf("config %s: exclude %q: %w", path, e, err)
		}
	}
	return c, nil
}

func resolve(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// Excluded reports whether path (a file or directory) matches an Exclude
// entry: the entry itself, anything below it, or a glob match.
func (c *Config) Excluded(path string) bool {
	if len(c.Exclude) == 0 {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, e := range c.Exclude {
		pat, err := filepath.Abs(e)
		if err != nil {
			continue
		}
		if abs == pat || strings.HasPrefix(abs, pat+string(filepath.Separator)) {
			return true
		}
		if ok, _ := filepath.Match(pat, abs); ok {
			return true
		}
	}
	return false
}

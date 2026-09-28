// Package yamlx contains small helpers for navigating and editing yaml.v3
// node trees without losing comments or key ordering.
package yamlx

import (
	"strconv"

	"gopkg.in/yaml.v3"
)

// Get returns the value node stored under key in mapping node m, or nil.
func Get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// Path walks nested mappings following keys.
func Path(m *yaml.Node, keys ...string) *yaml.Node {
	cur := m
	for _, k := range keys {
		cur = Get(cur, k)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// String returns the scalar value under key, or "".
func String(m *yaml.Node, keys ...string) string {
	n := Path(m, keys...)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// IsTrue reports whether n is a scalar boolean true.
func IsTrue(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	b, err := strconv.ParseBool(n.Value)
	return err == nil && b
}

// IsFalse reports whether n is a scalar boolean false.
func IsFalse(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	b, err := strconv.ParseBool(n.Value)
	return err == nil && !b
}

// Set stores value under key in mapping m, replacing an existing value in
// place (keeping its position and comments) or appending a new pair.
func Set(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			value.HeadComment = m.Content[i+1].HeadComment
			value.LineComment = m.Content[i+1].LineComment
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, Str(key), value)
}

// Delete removes key from mapping m. It reports whether the key existed.
func Delete(m *yaml.Node, key string) bool {
	if m == nil {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// EnsureMap returns the mapping stored under key, creating it (or replacing
// a null/non-mapping value) when needed.
func EnsureMap(m *yaml.Node, key string) *yaml.Node {
	if n := Get(m, key); n != nil && n.Kind == yaml.MappingNode {
		return n
	}
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	Set(m, key, n)
	return n
}

// Str builds a plain string scalar node.
func Str(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// Bool builds a boolean scalar node.
func Bool(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(b)}
}

// Seq builds a sequence of string scalars.
func Seq(values ...string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		n.Content = append(n.Content, Str(v))
	}
	return n
}

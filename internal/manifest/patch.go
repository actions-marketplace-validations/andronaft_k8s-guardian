package manifest

import (
	"bytes"
	"reflect"

	"go.yaml.in/yaml/v3"
)

// patchText writes the changes a fix made to doc as edits of the document's
// source text, so that a fix changes only the lines it has to:
//
//   - keys added to a block mapping are inserted above the mapping's first
//     key that starts a line, with its indentation;
//   - a scalar that replaced a plain scalar on its key's line is replaced in
//     place;
//   - a mapping that replaced "{}" or a null value is written below its key.
//
// text is the document's source and first the file line it starts on (the
// positions in doc are file positions). patchText returns nil when a change
// can't be written this way, or when the patched text doesn't decode to the
// same data as doc. The caller then re-encodes the document.
func patchText(text []byte, first int, doc *yaml.Node, compact bool, nl []byte) []byte {
	var src yaml.Node
	if err := yaml.Unmarshal(text, &src); err != nil || len(src.Content) == 0 || len(doc.Content) == 0 {
		return nil
	}
	p := &patcher{
		lines:   bytes.SplitAfter(text, []byte("\n")),
		first:   first,
		at:      map[[2]int]*yaml.Node{},
		value:   map[*yaml.Node]*yaml.Node{},
		compact: compact,
		nl:      nl,
		insert:  map[int][]byte{},
		replace: map[int]edit{},
	}
	p.index(src.Content[0])
	if !p.walk(doc.Content[0]) {
		return nil
	}
	var out bytes.Buffer
	for i, l := range p.lines {
		out.Write(p.insert[i])
		if e, ok := p.replace[i]; ok {
			out.Write(l[:e.from])
			out.Write(e.text)
			l = l[e.to:]
		}
		out.Write(l)
	}
	out.Write(p.insert[len(p.lines)])
	if !sameData(out.Bytes(), doc) {
		return nil
	}
	return out.Bytes()
}

type edit struct {
	from, to int // byte range replaced in the line
	text     []byte
}

type patcher struct {
	lines   [][]byte
	first   int
	at      map[[2]int]*yaml.Node     // file line and column -> source node (moved to file lines)
	value   map[*yaml.Node]*yaml.Node // source key -> source value
	compact bool
	nl      []byte
	insert  map[int][]byte // line index -> text inserted above it
	replace map[int]edit   // line index -> one edit in that line
}

// index moves the source nodes to file lines, like the nodes of the fixed
// document, and records them by position.
func (p *patcher) index(n *yaml.Node) {
	n.Line += p.first - 1
	p.at[[2]int{n.Line, n.Column}] = n
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			p.value[n.Content[i]] = n.Content[i+1]
		}
	}
	for _, c := range n.Content {
		p.index(c)
	}
}

// line returns the index and text of the source line a node starts on.
func (p *patcher) line(n *yaml.Node) (int, []byte) {
	i := n.Line - p.first
	if n.Line == 0 || i < 0 || i >= len(p.lines) {
		return -1, nil
	}
	return i, p.lines[i]
}

func (p *patcher) walk(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.MappingNode:
		var added []*yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			switch {
			case k.Line == 0:
				added = append(added, k, v)
			case v.Line == 0:
				if !p.replaced(k, v) {
					return false
				}
			case v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle != 0 && hasNewKeys(v):
				if !p.filled(k, v) {
					return false
				}
			default:
				if !p.walk(v) {
					return false
				}
			}
		}
		return len(added) == 0 || (n.Style&yaml.FlowStyle == 0 && p.insertKeys(n, added))
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Line == 0 || !p.walk(c) {
				return false
			}
		}
		return true
	case yaml.ScalarNode:
		s := p.at[[2]int{n.Line, n.Column}]
		return s != nil && s.Kind == yaml.ScalarNode && s.Value == n.Value && s.Tag == n.Tag
	}
	return false // aliases and anything unexpected
}

func hasNewKeys(m *yaml.Node) bool {
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Line == 0 {
			return true
		}
	}
	return false
}

// insertKeys inserts new entries above the first key of m that starts its
// own line (the key after "- " doesn't), with that key's indentation.
func (p *patcher) insertKeys(m *yaml.Node, added []*yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		at, l := p.line(k)
		if l == nil || k.Column-1 > len(l) || len(bytes.TrimLeft(l[:k.Column-1], " ")) != 0 {
			continue
		}
		// The key's comment lines stay directly above it.
		for at > 0 && bytes.HasPrefix(bytes.TrimLeft(p.lines[at-1], " "), []byte("#")) {
			at--
		}
		return p.render(at, k.Column-1, added)
	}
	return false
}

// filled writes the entries of a "{}" mapping that a fix filled below its key.
func (p *patcher) filled(k, m *yaml.Node) bool {
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Line != 0 {
			return false // the mapping wasn't empty
		}
	}
	return p.below(k, m, m.Content)
}

// replaced handles an existing key whose value was replaced by a new node.
func (p *patcher) replaced(k, v *yaml.Node) bool {
	old := p.value[p.at[[2]int{k.Line, k.Column}]]
	if old == nil {
		return false
	}
	switch {
	case v.Kind == yaml.ScalarNode && old.Kind == yaml.ScalarNode:
		return p.scalar(old, v)
	case v.Kind == yaml.MappingNode && old.Kind == yaml.ScalarNode && old.Tag == "!!null":
		return p.below(k, old, v.Content)
	}
	return false
}

// scalar replaces a plain single-line scalar with the encoding of v.
func (p *patcher) scalar(old, v *yaml.Node) bool {
	at, l := p.line(old)
	from := old.Column - 1
	to := from + len(old.Value)
	if l == nil || old.Style != 0 || to > len(l) || string(l[from:to]) != old.Value || !endsToken(l[to:]) {
		return false
	}
	// The line keeps its comment; Set copied it onto v as well.
	bare := *v
	bare.HeadComment, bare.LineComment, bare.FootComment = "", "", ""
	enc, err := encodeStyled(&bare, p.compact)
	if err != nil || bytes.Count(enc, []byte("\n")) != 1 {
		return false
	}
	return p.edit(at, edit{from, to, bytes.TrimSuffix(enc, []byte("\n"))})
}

// below replaces the value old of key k ("{}", a null or nothing, ending
// its line) with entries on new lines below the key, one level deeper.
func (p *patcher) below(k, old *yaml.Node, entries []*yaml.Node) bool {
	at, l := p.line(k)
	if l == nil {
		return false
	}
	if old.Line > k.Line {
		// The value is on a line of its own ("{}" from Helm's toYaml |
		// nindent): the entries take that line's place.
		vat, vl := p.line(old)
		if vl == nil || !emptyValue(vl) {
			return false
		}
		return p.edit(vat, edit{0, len(vl), nil}) && p.render(vat, k.Column+1, entries)
	}
	body := bytes.TrimRight(l, "\r\n")
	from := len(body)
	if old.Line == k.Line {
		from = old.Column - 1
	}
	if from > len(body) {
		return false
	}
	if !emptyValue(body[from:]) {
		return false // a comment or more text follows on the line
	}
	head := append(bytes.TrimRight(append([]byte{}, body[:from]...), " "), p.nl...)
	return p.edit(at, edit{0, len(l), head}) && p.render(at+1, k.Column+1, entries)
}

// emptyValue reports whether text is an empty value: "{}", a null or nothing.
func emptyValue(text []byte) bool {
	switch string(bytes.TrimSpace(text)) {
	case "", "{}", "null", "~", "Null", "NULL":
		return true
	}
	return false
}

func (p *patcher) edit(at int, e edit) bool {
	if _, taken := p.replace[at]; taken {
		return false
	}
	p.replace[at] = e
	return true
}

// render encodes key/value pairs, indents them and inserts them above line
// index at.
func (p *patcher) render(at, indent int, entries []*yaml.Node) bool {
	enc, err := encodeStyled(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: entries}, p.compact)
	if err != nil {
		return false
	}
	pad := bytes.Repeat([]byte(" "), indent)
	for _, l := range bytes.SplitAfter(enc, []byte("\n")) {
		if l = bytes.TrimSuffix(l, []byte("\n")); len(l) > 0 {
			p.insert[at] = append(append(append(p.insert[at], pad...), l...), p.nl...)
		}
	}
	return true
}

// endsToken reports whether rest (what follows a scalar on its line) starts
// with the end of the token.
func endsToken(rest []byte) bool {
	return len(rest) == 0 || bytes.IndexByte([]byte(" \t\r\n"), rest[0]) >= 0
}

// sameData reports whether text decodes to the same data as doc. Comments,
// formatting and key order don't count.
func sameData(text []byte, doc *yaml.Node) bool {
	enc, err := encodeDoc(doc)
	if err != nil {
		return false
	}
	var a, b any
	if yaml.Unmarshal(text, &a) != nil || yaml.Unmarshal(enc, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

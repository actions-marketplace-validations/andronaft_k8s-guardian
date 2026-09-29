// Package export turns k8s-guardian rules into cluster admission policies
// (Kubernetes ValidatingAdmissionPolicy with CEL), so the same guardrails
// that run in CI are enforced by the API server.
package export

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/custom"
)

var ident = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// celReserved are identifiers that can't be used after a dot in CEL.
var celReserved = map[string]bool{"in": true, "as": true, "break": true, "const": true, "continue": true, "else": true,
	"for": true, "function": true, "if": true, "import": true, "let": true, "loop": true, "package": true,
	"namespace": true, "return": true, "var": true, "void": true, "while": true, "true": true, "false": true, "null": true}

func quote(s string) string { return strconv.Quote(s) }

func and(parts ...string) string {
	var ps []string
	for _, p := range parts {
		if p != "" && p != "true" {
			ps = append(ps, p)
		}
	}
	switch len(ps) {
	case 0:
		return "true"
	case 1:
		return ps[0]
	}
	for i, p := range ps {
		ps[i] = paren(p)
	}
	return strings.Join(ps, " && ")
}

func or(parts ...string) string {
	for i, p := range parts {
		parts[i] = paren(p)
	}
	return strings.Join(parts, " || ")
}

func not(p string) string { return "!" + paren(p) }

var call = regexp.MustCompile(`^!?[A-Za-z_][A-Za-z0-9_.]*\(`)

// paren wraps p in parentheses unless it is already atomic: an identifier,
// a fully parenthesised expression or a single (optionally negated) call.
func paren(p string) string {
	if ident.MatchString(p) {
		return p
	}
	if m := call.FindString(p); m != "" || strings.HasPrefix(p, "(") || strings.HasPrefix(p, "!(") {
		open := strings.Index(p, "(")
		if strings.HasSuffix(p, ")") && closes(p, open) == len(p)-1 {
			return p
		}
	}
	return "(" + p + ")"
}

// closes returns the index of the parenthesis closing the one at open.
func closes(s string, open int) int {
	depth := 0
	inStr := false
	for i := open; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && inStr:
			i++
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

type gen struct{ vars int }

func (g *gen) fresh() string {
	g.vars++
	return fmt.Sprintf("x%d", g.vars)
}

// cond translates one condition evaluated relative to base into CEL with the
// same semantics as the in-process engine (see custom.compiledCond.holds).
func (g *gen) cond(base string, c custom.Condition) (string, error) {
	segs, err := custom.ParsePath(c.Path)
	if err != nil {
		return "", err
	}
	return g.pred(base, segs, c)
}

// pred walks the path, collecting presence guards, until the end or a [*].
func (g *gen) pred(base string, segs []custom.Segment, c custom.Condition) (string, error) {
	cur := base
	var guards []string
	for i, s := range segs {
		switch {
		case s.All:
			list := cur
			e := g.fresh()
			inner, err := g.pred(e, segs[i+1:], c)
			if err != nil {
				return "", err
			}
			present := and(append(guards, fmt.Sprintf("size(%s) > 0", list))...)
			all := fmt.Sprintf("%s.all(%s, %s)", list, e, inner)
			switch c.Op {
			case "exists", "matches", "equals", "in":
				return and(present, all), nil
			default:
				return or(not(present), all), nil
			}
		case s.Index >= 0 && s.Key == "":
			guards = append(guards, fmt.Sprintf("size(%s) > %d", cur, s.Index))
			cur = fmt.Sprintf("%s[%d]", cur, s.Index)
		case ident.MatchString(s.Key) && !celReserved[s.Key]:
			guards = append(guards, fmt.Sprintf("has(%s.%s)", cur, s.Key))
			cur = cur + "." + s.Key
		default:
			guards = append(guards, fmt.Sprintf("%s in %s", quote(s.Key), cur))
			cur = fmt.Sprintf("%s[%s]", cur, quote(s.Key))
		}
	}
	return leaf(and(guards...), cur, c)
}

func leaf(present, v string, c custom.Condition) (string, error) {
	str := fmt.Sprintf("string(%s)", v)
	list := func() string {
		q := make([]string, len(c.Values))
		for i, x := range c.Values {
			q[i] = quote(x)
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	cmp := map[string]string{"gt": "> 0", "gte": ">= 0", "lt": "< 0", "lte": "<= 0"}
	switch c.Op {
	case "exists":
		return present, nil
	case "notExists":
		return not(present), nil
	case "equals":
		return and(present, fmt.Sprintf("%s == %s", str, quote(c.Value))), nil
	case "notEquals":
		return or(not(present), fmt.Sprintf("%s != %s", str, quote(c.Value))), nil
	case "in":
		return and(present, fmt.Sprintf("%s in %s", str, list())), nil
	case "notIn":
		return or(not(present), fmt.Sprintf("!(%s in %s)", str, list())), nil
	case "matches":
		return and(present, fmt.Sprintf("%s.matches(%s)", str, quote(c.Value))), nil
	case "notMatches":
		return or(not(present), fmt.Sprintf("!%s.matches(%s)", str, quote(c.Value))), nil
	case "gt", "gte", "lt", "lte":
		// Kubernetes CEL quantity library; plain numbers are valid quantities.
		return or(not(present), fmt.Sprintf("quantity(%s).compareTo(quantity(%s)) %s", str, quote(c.Value), cmp[c.Op])), nil
	}
	return "", fmt.Errorf("unsupported op %q", c.Op)
}

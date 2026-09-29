package tui

import "strings"

// Op is a diff line operation.
type Op byte

const (
	Same Op = ' '
	Add  Op = '+'
	Del  Op = '-'
)

// Line is one line of a unified diff.
type Line struct {
	Op   Op
	Text string
}

// LineDiff computes a line diff of a and b using an LCS table.
func LineDiff(a, b string) []Line {
	x := strings.Split(strings.TrimRight(a, "\n"), "\n")
	y := strings.Split(strings.TrimRight(b, "\n"), "\n")
	n, m := len(x), len(y)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []Line
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			out = append(out, Line{Same, x[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, Line{Del, x[i]})
			i++
		default:
			out = append(out, Line{Add, y[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, Line{Del, x[i]})
	}
	for ; j < m; j++ {
		out = append(out, Line{Add, y[j]})
	}
	return out
}

// Hunks trims unchanged lines to at most ctx lines around changes.
func Hunks(lines []Line, ctx int) []Line {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op != Same {
			for k := max(0, i-ctx); k <= min(len(lines)-1, i+ctx); k++ {
				keep[k] = true
			}
		}
	}
	var out []Line
	skipped := false
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
			skipped = false
		} else if !skipped {
			out = append(out, Line{Same, "…"})
			skipped = true
		}
	}
	return out
}

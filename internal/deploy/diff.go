// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// diff renders a minimal line diff ("+" added, "-" removed, unchanged lines
// within 2 of a change as context). Files are small, so plain LCS is fine.
func diff(before, after []byte) string {
	x := splitLines(string(before))
	y := splitLines(string(after))
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
	type op struct {
		c    byte
		line string
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			ops = append(ops, op{' ', x[i]})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', x[i]})
			i++
		default:
			ops = append(ops, op{'+', y[j]})
			j++
		}
	}
	const ctx = 2
	var b strings.Builder
	last := -1
	for k, o := range ops {
		if o.c == ' ' {
			near := false
			for d := -ctx; d <= ctx; d++ {
				if k+d >= 0 && k+d < len(ops) && ops[k+d].c != ' ' {
					near = true
					break
				}
			}
			if !near {
				continue
			}
		}
		if last >= 0 && k > last+1 {
			b.WriteString("  …\n")
		}
		b.WriteByte(o.c)
		b.WriteByte(' ')
		b.WriteString(o.line)
		b.WriteByte('\n')
		last = k
	}
	return b.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func fingerprint(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:3])
}

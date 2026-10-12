package mask

import (
	"sort"
	"strings"
	"unicode"
)

// Masker holds the secret values to hide. It is read-only after New, so Mask
// is safe to call from several goroutines.
type Masker struct{ vals []string }

// New masks each secret, and each line of a multi-line secret, because tools
// often print one line of a key at a time. Short lines are kept because a
// short tail of a key, such as "AQAB==", would otherwise leak. Armour lines
// and lines with no letter or digit are skipped because they are not secret
// and would mask structural text such as "{" or "}".
func New(secrets []string) *Masker {
	seen := map[string]bool{}
	var vals []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			vals = append(vals, v)
		}
	}
	for _, s := range secrets {
		add(s)
		if strings.Contains(s, "\n") {
			for _, l := range strings.Split(s, "\n") {
				l = strings.TrimSpace(l)
				if l == "" || strings.HasPrefix(l, "-----") || !hasLetterOrDigit(l) {
					continue
				}
				add(l)
			}
		}
	}
	return &Masker{vals}
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

type span struct{ start, end int }

// Mask replaces every occurrence of every secret with "***". Occurrences that
// overlap or touch are merged into one "***", so a secret that overlaps
// another cannot leave part of itself visible.
func (m *Masker) Mask(s string) string {
	if m == nil || len(m.vals) == 0 {
		return s
	}
	var spans []span
	for _, v := range m.vals {
		for i := 0; i <= len(s)-len(v); {
			idx := strings.Index(s[i:], v)
			if idx < 0 {
				break
			}
			start := i + idx
			spans = append(spans, span{start, start + len(v)})
			i = start + 1
		}
	}
	if len(spans) == 0 {
		return s
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	pos := 0
	for i := 0; i < len(spans); {
		start, end := spans[i].start, spans[i].end
		i++
		for i < len(spans) && spans[i].start <= end {
			if spans[i].end > end {
				end = spans[i].end
			}
			i++
		}
		b.WriteString(s[pos:start])
		b.WriteString("***")
		pos = end
	}
	b.WriteString(s[pos:])
	return b.String()
}

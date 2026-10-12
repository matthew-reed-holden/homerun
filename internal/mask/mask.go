package mask

import (
	"sort"
	"strings"
)

type Masker struct{ r *strings.Replacer }

// New masks each secret, and each line of a multi-line secret, because tools
// often print one line of a key at a time.
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
				// Short lines such as PEM armour are not secret and would mask too much.
				if l = strings.TrimSpace(l); len(l) >= 8 && !strings.HasPrefix(l, "-----") {
					add(l)
				}
			}
		}
	}
	// strings.Replacer tries pairs in argument order, so longer secrets go first.
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	pairs := make([]string, 0, 2*len(vals))
	for _, v := range vals {
		pairs = append(pairs, v, "***")
	}
	return &Masker{strings.NewReplacer(pairs...)}
}

func (m *Masker) Mask(s string) string {
	if m == nil || m.r == nil {
		return s
	}
	return m.r.Replace(s)
}

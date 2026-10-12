package workflow

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ForkUnsafeJobs lists the jobs a fork PR must not run, in file order: jobs
// aimed at the host or at self-hosted runners, jobs whose container or
// services set options or volumes (act honours --privileged and -v /:/host
// there), and reusable-workflow jobs, whose called jobs homerun cannot vet.
// Anything it cannot read statically (an expression standing in for a whole
// container or services value, a merge key) counts as unsafe.
func ForkUnsafeJobs(root, rel string) ([]string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := readRegular(r, rel)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Jobs yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	jobs := resolve(&doc.Jobs)
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: jobs is not a mapping", rel)
	}
	var out []string
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		id := jobs.Content[i].Value
		var j struct {
			RunsOn    yaml.Node `yaml:"runs-on"`
			Container yaml.Node `yaml:"container"`
			Services  yaml.Node `yaml:"services"`
			Strategy  yaml.Node `yaml:"strategy"`
			Uses      string    `yaml:"uses"`
		}
		if err := jobs.Content[i+1].Decode(&j); err != nil {
			return nil, fmt.Errorf("%s: jobs.%s: %w", rel, id, err)
		}
		if j.Uses != "" || runsOnUnsafe(&j.RunsOn, &j.Strategy) || containerUnsafe(&j.Container) || servicesUnsafe(&j.Services) {
			out = append(out, id)
		}
	}
	return out, nil
}

func readRegular(r *os.Root, rel string) ([]byte, error) {
	fi, err := r.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", rel)
	}
	return r.ReadFile(rel)
}

func resolve(n *yaml.Node) *yaml.Node {
	for depth := 0; n != nil && n.Kind == yaml.AliasNode && depth < 32; depth++ {
		n = n.Alias
	}
	return n
}

// anyScalar reports whether f holds for any scalar in n, keys included.
func anyScalar(n *yaml.Node, f func(string) bool) bool {
	n = resolve(n)
	if n == nil {
		return false
	}
	if n.Kind == yaml.ScalarNode {
		return f(n.Value)
	}
	for _, c := range n.Content {
		if anyScalar(c, f) {
			return true
		}
	}
	return false
}

func isExpr(s string) bool { return strings.Contains(s, "${{") }

func hostLabel(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "self-hosted") || strings.Contains(s, HostLabel)
}

func runsOnUnsafe(runsOn, strategy *yaml.Node) bool {
	if anyScalar(runsOn, hostLabel) || mapGet(resolve(runsOn), "group") != nil {
		return true
	}
	// runs-on: ${{ matrix.os }} picks its label from the matrix.
	return anyScalar(runsOn, isExpr) && anyScalar(strategy, hostLabel)
}

func containerUnsafe(n *yaml.Node) bool {
	n = resolve(n)
	switch {
	case n == nil:
		return false
	case n.Kind == yaml.ScalarNode:
		return isExpr(n.Value)
	case n.Kind == yaml.MappingNode:
		return checkKeys(n) != nil || mapGet(n, "options") != nil || mapGet(n, "volumes") != nil
	}
	return false
}

func servicesUnsafe(n *yaml.Node) bool {
	n = resolve(n)
	switch {
	case n == nil:
		return false
	case n.Kind == yaml.ScalarNode:
		return isExpr(n.Value)
	case n.Kind == yaml.MappingNode:
		if checkKeys(n) != nil {
			return true
		}
		for i := 1; i < len(n.Content); i += 2 {
			if containerUnsafe(n.Content[i]) {
				return true
			}
		}
	}
	return false
}

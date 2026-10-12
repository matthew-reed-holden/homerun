package workflow

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// ForkUnsafeJobs lists the jobs a fork PR must not run, in file order: jobs
// aimed at the host or at self-hosted runners, and jobs whose container or
// services set options or volumes (act honours --privileged and -v /:/host
// there). Anything it cannot read statically (an expression standing in for a
// whole container or services value) counts as unsafe, and merge keys,
// duplicate keys or non-mapping jobs are an error. A
// reusable-workflow job is safe only if it calls a local workflow, read from
// the same snapshot, none of whose jobs is unsafe; remote callees cannot be
// vetted.
func ForkUnsafeJobs(root, rel string) ([]string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	fc := &forkChecker{r: r, callees: map[string]bool{}}
	return fc.unsafeJobs(rel, 0)
}

// maxCallDepth matches GitHub's limit on nested reusable workflows.
const maxCallDepth = 4

// errShape marks YAML this check refuses to read: merge keys, duplicate keys
// and non-mapping jobs can make act (which decodes into structs) see jobs that
// a walk over the raw nodes does not. A fork run with one fails setup.
var errShape = errors.New("unsupported workflow shape for a fork PR")

type forkChecker struct {
	r       *os.Root
	callees map[string]bool // callee path -> safe; parsed once per call, and cycles end here
	reads   int
}

func (fc *forkChecker) unsafeJobs(rel string, depth int) ([]string, error) {
	fc.reads++
	b, err := readRegular(fc.r, rel)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("%s: empty workflow", rel)
	}
	top := resolve(root.Content[0])
	if top.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: %w: not a mapping", rel, errShape)
	}
	if err := checkKeys(top); err != nil {
		return nil, fmt.Errorf("%s: %w: %v", rel, errShape, err)
	}
	jobs := resolve(mapGet(top, "jobs"))
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: %w: jobs is not a mapping", rel, errShape)
	}
	if err := checkKeys(jobs); err != nil {
		return nil, fmt.Errorf("%s: jobs: %w: %v", rel, errShape, err)
	}
	var out []string
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		id, body := jobs.Content[i].Value, resolve(jobs.Content[i+1])
		if body == nil || body.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: jobs.%s: %w: not a mapping", rel, id, errShape)
		}
		if err := checkKeys(body); err != nil {
			return nil, fmt.Errorf("%s: jobs.%s: %w: %v", rel, id, errShape, err)
		}
		var j struct {
			RunsOn    yaml.Node `yaml:"runs-on"`
			Container yaml.Node `yaml:"container"`
			Services  yaml.Node `yaml:"services"`
			Strategy  yaml.Node `yaml:"strategy"`
			Uses      string    `yaml:"uses"`
		}
		if err := body.Decode(&j); err != nil {
			return nil, fmt.Errorf("%s: jobs.%s: %w", rel, id, err)
		}
		unsafe := runsOnUnsafe(&j.RunsOn, &j.Strategy) || containerUnsafe(&j.Container) || servicesUnsafe(&j.Services)
		if j.Uses != "" && !unsafe {
			safe, err := fc.safeCallee(j.Uses, depth)
			if err != nil {
				return nil, err
			}
			unsafe = !safe
		}
		if unsafe {
			out = append(out, id)
		}
	}
	return out, nil
}

// safeCallee vets a reusable workflow. Remote, missing or unparsable callees
// are unsafe (the job is skipped); a callee with a refused shape is an error.
func (fc *forkChecker) safeCallee(uses string, depth int) (bool, error) {
	rel, local := strings.CutPrefix(uses, "./")
	if !local || isExpr(uses) || depth+1 >= maxCallDepth {
		return false, nil
	}
	rel = path.Clean(rel)
	if safe, seen := fc.callees[rel]; seen {
		return safe, nil
	}
	fc.callees[rel] = false // a cycle back to rel reads as unsafe
	unsafe, err := fc.unsafeJobs(rel, depth+1)
	if errors.Is(err, errShape) {
		return false, err
	}
	safe := err == nil && len(unsafe) == 0
	fc.callees[rel] = safe
	return safe, nil
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

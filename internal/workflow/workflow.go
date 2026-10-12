package workflow

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringList accepts a YAML sequence or a single scalar, as GitHub does.
type StringList []string

func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = StringList{n.Value}
		return nil
	}
	var s []string
	if err := n.Decode(&s); err != nil {
		return err
	}
	*l = s
	return nil
}

type Filter struct {
	Branches       StringList `yaml:"branches"`
	BranchesIgnore StringList `yaml:"branches-ignore"`
	Tags           StringList `yaml:"tags"`
	TagsIgnore     StringList `yaml:"tags-ignore"`
	Paths          StringList `yaml:"paths"`
	PathsIgnore    StringList `yaml:"paths-ignore"`
	Types          StringList `yaml:"types"`
}

type Job struct {
	ID   string
	Name string
}

type Workflow struct {
	Path string // relative to the repo root
	Name string
	On   map[string]*Filter // a nil filter means the event has no filters
	Jobs []Job
}

func LoadDir(root string) ([]*Workflow, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d, err := r.Open(".github/workflows")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ents, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") {
			rels = append(rels, ".github/workflows/"+n)
		}
	}
	sort.Strings(rels)
	out := make([]*Workflow, 0, len(rels))
	for _, rel := range rels {
		w, err := Load(root, rel)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

func Load(root, rel string) (*Workflow, error) {
	// The snapshot is an untrusted commit: os.Root refuses any symlink that
	// escapes it, and the leaf must be a regular file so error snippets cannot leak host files.
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	fi, err := r.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", rel)
	}
	b, err := r.ReadFile(rel)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Name string    `yaml:"name"`
		On   yaml.Node `yaml:"on"`
		Jobs yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	w := &Workflow{Path: rel, Name: doc.Name, On: map[string]*Filter{}}
	if w.Name == "" {
		w.Name = rel // GitHub shows the file path for unnamed workflows
	}
	switch doc.On.Kind {
	case yaml.ScalarNode:
		w.On[doc.On.Value] = nil
	case yaml.SequenceNode:
		for _, n := range doc.On.Content {
			w.On[n.Value] = nil
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(doc.On.Content); i += 2 {
			ev, val := doc.On.Content[i].Value, doc.On.Content[i+1]
			var f *Filter
			if val.Kind == yaml.MappingNode {
				f = &Filter{}
				if err := val.Decode(f); err != nil {
					return nil, fmt.Errorf("%s: on.%s: %w", rel, ev, err)
				}
			}
			if f != nil {
				if err := f.validate(); err != nil {
					return nil, fmt.Errorf("%s: on.%s: %w", rel, ev, err)
				}
			}
			w.On[ev] = f
		}
	default:
		return nil, fmt.Errorf("%s: missing 'on'", rel)
	}
	// act decodes jobs into a map, so an alias, merge key or duplicate key
	// would make it see different jobs than a walk over the raw nodes.
	jobs := resolve(&doc.Jobs)
	switch {
	case jobs == nil || jobs.Kind == 0:
	case jobs.Kind != yaml.MappingNode:
		return nil, fmt.Errorf("%s: jobs is not a mapping", rel)
	default:
		if err := checkKeys(jobs); err != nil {
			return nil, fmt.Errorf("%s: jobs: %w", rel, err)
		}
	}
	for i := 0; jobs != nil && i+1 < len(jobs.Content); i += 2 {
		id := jobs.Content[i].Value
		var j struct {
			Name string `yaml:"name"`
		}
		if err := jobs.Content[i+1].Decode(&j); err != nil {
			return nil, fmt.Errorf("%s: jobs.%s: %w", rel, id, err)
		}
		name := j.Name
		// ponytail: expression names are not evaluated; the id is stable and unique. Evaluate if users ask.
		if name == "" || strings.Contains(name, "${{") {
			name = id
		}
		w.Jobs = append(w.Jobs, Job{ID: id, Name: name})
	}
	return w, nil
}

func (w *Workflow) Handles() bool {
	_, push := w.On["push"]
	_, pr := w.On["pull_request"]
	return push || pr
}

func (w *Workflow) Job(id string) (Job, bool) {
	for _, j := range w.Jobs {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

func (f *Filter) validate() error {
	for _, l := range []StringList{f.Branches, f.BranchesIgnore, f.Tags, f.TagsIgnore, f.Paths, f.PathsIgnore} {
		for _, p := range l {
			if _, err := compileGlob(strings.TrimPrefix(p, "!")); err != nil {
				return fmt.Errorf("invalid pattern %q", p)
			}
		}
	}
	return nil
}

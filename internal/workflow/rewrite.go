package workflow

import (
	"fmt"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

// HostLabel is the runs-on label homerun maps to act's -self-hosted platform.
// act's -P maps labels, not jobs, so host jobs get a label of their own.
const HostLabel = "homerun-host"

// PrepareJobs edits a workflow in the snapshot (never the user's checkout):
// host jobs run on the host, disabled jobs get "if: false" so act skips them.
// The snapshot is an untrusted commit, so rel is read and written through
// os.Root and must be a regular file; a committed symlink cannot redirect the
// write to a host file.
func PrepareJobs(root, rel string, host, disable []string) error {
	if len(host) == 0 && len(disable) == 0 {
		return nil
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	fi, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", rel)
	}
	b, err := r.ReadFile(rel)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("%s: empty workflow", rel)
	}
	jobs := mapGet(doc.Content[0], "jobs")
	if jobs == nil {
		return nil
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		id, job := jobs.Content[i].Value, jobs.Content[i+1]
		switch {
		case slices.Contains(disable, id):
			mapSet(job, "if", "false")
		case slices.Contains(host, id):
			mapSet(job, "runs-on", HostLabel)
		}
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	f, err := r.OpenFile(rel, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key, val string) {
	v := &yaml.Node{Kind: yaml.ScalarNode, Value: val}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}

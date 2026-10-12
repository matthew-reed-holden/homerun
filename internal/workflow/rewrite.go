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
	// workflow.Load decodes through yaml.v3 struct decoding, which resolves merge keys
	// and tolerates the shapes below; refusing them keeps the file from being read two ways.
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: jobs is not a mapping; cannot rewrite %v", rel, slices.Concat(host, disable))
	}
	if err := checkKeys(jobs); err != nil {
		return fmt.Errorf("%s: jobs: %w", rel, err)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		id, job := jobs.Content[i].Value, jobs.Content[i+1]
		isDisable, isHost := slices.Contains(disable, id), slices.Contains(host, id)
		if !isDisable && !isHost {
			continue
		}
		seen[id] = true
		// An alias or null job would be skipped silently, leaving a fork-disabled job running.
		if job.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: jobs.%s: not a mapping; cannot rewrite", rel, id)
		}
		if err := checkKeys(job); err != nil {
			return fmt.Errorf("%s: jobs.%s: %w", rel, id, err)
		}
		switch {
		case isDisable:
			mapSet(job, "if", "false")
		case mapGet(job, "uses") != nil:
			return fmt.Errorf("%s: jobs.%s: reusable workflow jobs cannot run on the host", rel, id)
		default:
			mapSet(job, "runs-on", HostLabel)
		}
	}
	for _, id := range slices.Concat(host, disable) {
		if !seen[id] {
			return fmt.Errorf("%s: jobs.%s: not a direct key of jobs", rel, id)
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

func checkKeys(m *yaml.Node) error {
	keys := map[string]bool{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Tag == "!!merge" {
			return fmt.Errorf("merge key not supported")
		}
		if keys[k.Value] {
			return fmt.Errorf("duplicate key %q", k.Value)
		}
		keys[k.Value] = true
	}
	return nil
}

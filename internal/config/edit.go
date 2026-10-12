package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

func AddRepo(path, repo string) error {
	return edit(path, func(root *yaml.Node) {
		repos := ensureMap(root, "repos")
		if mapGet(repos, repo) == nil {
			repos.Content = append(repos.Content, str(repo), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle})
		}
	})
}

func SetApp(path string, id int64, keyCmd string) error {
	return edit(path, func(root *yaml.Node) {
		key := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle, Content: []*yaml.Node{str("cmd"), str(keyCmd)}}
		app := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			str("id"), {Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(id, 10)},
			str("private_key"), key,
		}}
		setKey(root, "app", app)
	})
}

// edit round-trips the file through yaml.Node so the user's comments survive.
func edit(path string, fn func(root *yaml.Node)) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(b)) > 0 {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: the top level must be a mapping", path)
	}
	fn(root)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return writeAtomic(path, out)
}

// writeAtomic renames a fsynced temp file into place so a crash leaves either
// the old config or the new one, never a truncated file holding secrets.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// CreateTemp opens 0600, so the content is never readable by others, even briefly.
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func str(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setKey(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, str(key), v)
}

func ensureMap(m *yaml.Node, key string) *yaml.Node {
	v := mapGet(m, key)
	if v == nil || v.Kind != yaml.MappingNode {
		v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setKey(m, key, v)
	}
	return v
}

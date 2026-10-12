package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

// Secret is a value from one of three sources. Plain strings are allowed:
// homerun warns about them but leaves secret handling to the user.
type Secret struct {
	Cmd   string
	Env   string
	Value string
}

func (s *Secret) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		s.Value = n.Value
		return nil
	case yaml.MappingNode:
		var m struct {
			Cmd string `yaml:"cmd"`
			Env string `yaml:"env"`
		}
		if err := n.Decode(&m); err != nil {
			return err
		}
		if (m.Cmd == "") == (m.Env == "") {
			return fmt.Errorf("line %d: secret needs exactly one of cmd or env", n.Line)
		}
		s.Cmd, s.Env = m.Cmd, m.Env
		return nil
	}
	return fmt.Errorf("line %d: secret must be a string, {cmd: ...} or {env: ...}", n.Line)
}

func (s Secret) Plaintext() bool { return s.Cmd == "" && s.Env == "" }

func (s Secret) Resolve(ctx context.Context) (string, error) {
	switch {
	case s.Cmd != "":
		out, err := exec.CommandContext(ctx, "sh", "-c", s.Cmd).Output()
		if err != nil {
			// The error carries only the exit status; stdout may hold part of the secret.
			return "", fmt.Errorf("command failed: %w", err)
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	case s.Env != "":
		v, ok := os.LookupEnv(s.Env)
		if !ok {
			return "", fmt.Errorf("environment variable %s is not set", s.Env)
		}
		return v, nil
	}
	return s.Value, nil
}

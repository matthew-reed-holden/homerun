package git

import "testing"

func TestParseGitHubRemote(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:o/n.git":         "o/n",
		"git@github.com:o/n":             "o/n",
		"ssh://git@github.com/o/n.git":   "o/n",
		"ssh://git@github.com:22/o/n":    "o/n",
		"https://github.com/o/n.git":     "o/n",
		"https://u@github.com/o/n/":      "o/n",
		"https://GitHub.com/O/N":         "O/N",
		"https://github.com/o/n.git.git": "o/n.git",
	} {
		if got, ok := ParseGitHubRemote(in); !ok || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"https://gitlab.com/o/n", "git@ghe.corp:o/n", "https://github.com/o", "https://github.com/o/n/extra", ""} {
		if got, ok := ParseGitHubRemote(in); ok {
			t.Errorf("%q matched %q", in, got)
		}
	}
}

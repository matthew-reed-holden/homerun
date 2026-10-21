package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTabsValidated(t *testing.T) {
	six := "tabs:\n"
	for i := 0; i < 6; i++ {
		six += "  - {name: T, query: \"is:open\"}\n"
	}
	for _, tc := range []struct{ name, body, want string }{
		{"no name", "tabs: [{name: \"\", query: \"is:open\"}]\n", "tabs[0]: name is required"},
		{"no query", "tabs: [{name: Mine, query: \"\"}]\n", "tabs[0]: query is required"},
		{"six", six, "at most 5 tabs"},
	} {
		p := filepath.Join(t.TempDir(), "config.yml")
		writeFile(t, p, tc.body, 0o600)
		if _, _, err := LoadUser(p); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: err %v, want %q", tc.name, err, tc.want)
		}
	}
	p := filepath.Join(t.TempDir(), "config.yml")
	writeFile(t, p, "tabs:\n  - {name: Mine, query: \"is:open author:@me\"}\n", 0o600)
	u, _, err := LoadUser(p)
	if err != nil || len(u.Tabs) != 1 || u.Tabs[0] != (Tab{"Mine", "is:open author:@me"}) {
		t.Fatalf("%+v %v", u, err)
	}
	writeFile(t, p, "max_parallel: 2\n", 0o600)
	if u, _, err := LoadUser(p); err != nil || u.Tabs != nil {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestIconsWarning(t *testing.T) {
	for icons, want := range map[string]bool{"emoji": true, "ascii": false} {
		p := filepath.Join(t.TempDir(), "config.yml")
		if err := os.WriteFile(p, []byte("icons: "+icons+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, warns, err := LoadUser(p)
		if err != nil {
			t.Fatal(err)
		}
		got := false
		for _, w := range warns {
			if strings.Contains(w, `icons: "emoji" is not nerd, unicode or ascii; using nerd`) {
				got = true
			}
		}
		if got != want || (!want && len(warns) != 0) {
			t.Errorf("icons %s: warns %q", icons, warns)
		}
	}
}

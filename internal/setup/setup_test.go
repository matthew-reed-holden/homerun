package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-github/v92/github"
	"github.com/matthew-reed-holden/homerun/internal/config"
)

func TestManifest(t *testing.T) {
	var m map[string]any
	b, _ := json.Marshal(manifest("homerun-box-ab12", "http://127.0.0.1:1/callback", false))
	json.Unmarshal(b, &m)
	perms := m["default_permissions"].(map[string]any)
	if perms["checks"] != "write" || perms["contents"] != "read" || perms["pull_requests"] != "read" || perms["metadata"] != "read" || m["public"] != false {
		t.Fatalf("manifest: %s", b)
	}
	if _, ok := m["hook_attributes"]; ok {
		t.Fatal("v1 needs no webhook")
	}
	if n := appName("a---"); strings.Contains(n, "--") {
		t.Fatalf("app name %q", n)
	}
	if n := appName(""); strings.Contains(n, "--") || !strings.HasPrefix(n, "homerun-") {
		t.Fatalf("empty host app name %q", n)
	}
	if n := appName("My.Very_Long-Hostname.example.com"); len(n) > 34 || !strings.HasPrefix(n, "homerun-my-very-long-ho") {
		t.Fatalf("app name %q", n)
	}
}

func TestManifestHandler(t *testing.T) {
	codes := make(chan string, 1)
	srv := httptest.NewServer(handler("https://github.com/settings/apps/new?state=s1", `{"name":"x<y"}`, "s1", codes))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/")
	page, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(page), `action="https://github.com/settings/apps/new?state=s1"`) || strings.Contains(string(page), "x<y") {
		t.Fatalf("form page:\n%s", page)
	}
	if resp, _ := http.Get(srv.URL + "/callback?state=wrong&code=c"); resp.StatusCode != 400 {
		t.Fatalf("state mismatch accepted: %d", resp.StatusCode)
	}
	if resp, _ := http.Get(srv.URL + "/callback?state=s1&code=c1"); resp.StatusCode != 200 || <-codes != "c1" {
		t.Fatal("good callback not accepted")
	}
}

func TestUnit(t *testing.T) {
	u := Unit("/usr/bin/homerun", "/home/me/.config/homerun/config.yml", "/usr/bin:/bin")
	for _, want := range []string{`ExecStart="/usr/bin/homerun" daemon --config "/home/me/.config/homerun/config.yml"`,
		`Environment="PATH=/usr/bin:/bin"`, "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := map[[2]string]bool{
		{"0.2.89", "0.2.89"}: true, {"0.2.90", "0.2.89"}: true, {"0.3.0", "0.2.89"}: true,
		{"0.2.9", "0.2.89"}: false, {"garbage", "0.2.89"}: false,
	}
	for in, want := range cases {
		if got := versionAtLeast(in[0], in[1]); got != want {
			t.Errorf("versionAtLeast(%q, %q) = %v", in[0], in[1], got)
		}
	}
}

const testPEM = "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----\n"

func TestSaveAppPrintsKeyAndWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yml")
	var out bytes.Buffer
	if err := saveApp(cfg, "", 42, "homerun-x", "https://github.com/apps/homerun-x", testPEM, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "BEGIN HOMERUN APP KEY OUTPUT-----\n"+testPEM) {
		t.Fatalf("key not printed between markers:\n%s", out.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "config.yml" {
		t.Fatalf("unexpected files: %v", entries)
	}
	b, _ := os.ReadFile(cfg)
	if strings.Contains(string(b), "BEGIN") || strings.Contains(string(b), "private_key") {
		t.Fatalf("key or private_key written to config:\n%s", b)
	}
	u, _, err := config.LoadUser(cfg)
	if err != nil || u.App.ID != 42 {
		t.Fatalf("app id not saved: %v %+v", err, u)
	}
}

func TestSaveAppKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := filepath.Join(dir, "config.yml")
	// The quote and parent directory exercise both the shell escaping and the 0700 mkdir.
	keyFile := filepath.Join(dir, "keys d", "it's $(touch pwned).pem")
	var out bytes.Buffer
	if err := saveApp(cfg, keyFile, 7, "s", "https://github.com/apps/s", testPEM, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "BEGIN RSA") {
		t.Fatal("key printed in file mode")
	}
	fi, err := os.Stat(keyFile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	if di, _ := os.Stat(filepath.Dir(keyFile)); di.Mode().Perm() != 0o700 {
		t.Fatalf("parent dir mode %o", di.Mode().Perm())
	}
	u, _, err := config.LoadUser(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := u.App.PrivateKey.Resolve(context.Background())
	if err != nil || got != strings.TrimRight(testPEM, "\n") {
		t.Fatalf("private_key cmd resolved to %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("path injected a shell command")
	}
}

func TestWriteKeyFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("keep"), 0o600)
	link := filepath.Join(dir, "link.pem")
	os.Symlink(target, link)
	if err := writeKeyFile(link, []byte("x")); err == nil {
		t.Fatal("symlink overwritten")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("symlink target modified")
	}
}

func TestShellQuoteRoundTrips(t *testing.T) {
	for _, in := range []string{"plain", "it's", "'; touch x; '", "a b$c`d`"} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(in)).Output()
		if err != nil || string(out) != in {
			t.Errorf("shellQuote(%q) round-tripped to %q, %v", in, out, err)
		}
	}
}

func TestCreateAppFlow(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yml")
	var out bytes.Buffer
	open := func(base string) error {
		resp, err := http.Get(base)
		if err != nil {
			return err
		}
		page, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		state := regexp.MustCompile(`state=([0-9a-f]+)`).FindSubmatch(page)[1]
		resp, err = http.Get(strings.TrimSuffix(base, "/") + "/callback?code=c9&state=" + string(state))
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	exchange := func(_ context.Context, code string) (*github.AppConfig, error) {
		if code != "c9" {
			t.Errorf("code %q", code)
		}
		return &github.AppConfig{ID: github.Ptr(int64(5)), Slug: github.Ptr("s"), PEM: github.Ptr(testPEM), HTMLURL: github.Ptr("https://github.com/apps/s")}, nil
	}
	if err := createApp(context.Background(), "", false, cfg, "", open, &out, exchange); err != nil {
		t.Fatal(err)
	}
	if u, _, err := config.LoadUser(cfg); err != nil || u.App.ID != 5 {
		t.Fatalf("%v %+v", err, u)
	}
}

func TestUnitEscapesSpecials(t *testing.T) {
	u := Unit("/opt/my 100%/homerun", "/c/$HOME/c.yml", "/a%b")
	for _, want := range []string{`ExecStart="/opt/my 100%%/homerun"`, `"/c/$$HOME/c.yml"`, `Environment="PATH=/a%%b"`} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "Environment=PATH=/a%%b\nEnvironment") || strings.Count(u, "Environment=") != 1 {
		t.Error("unit should carry only PATH in its environment")
	}
}

func fakeTools(t *testing.T, actVersion string) {
	bin := t.TempDir()
	for name, body := range map[string]string{
		"git": "exit 0", "docker": "exit 0", "act": "echo act version " + actVersion,
	} {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func TestDoctor(t *testing.T) {
	fakeTools(t, "0.2.90")
	t.Setenv("HOMERUN_TEST_KEY", "SUPERSECRETKEYMATERIAL")
	cfg := filepath.Join(t.TempDir(), "config.yml")
	os.WriteFile(cfg, []byte("app:\n  id: 1\n  private_key: {env: HOMERUN_TEST_KEY}\n"), 0o644)
	var out bytes.Buffer
	err := Doctor(context.Background(), cfg, &out)
	s := out.String()
	if err == nil {
		t.Fatal("expected failure for loose mode and bad key")
	}
	for _, want := range []string{"✗ config file mode 600: mode is 644", "✓ GitHub App private key source resolves", "✗ GitHub App key valid", "! act 0.2.90 found", "✓ act on PATH"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "SUPERSECRETKEYMATERIAL") {
		t.Fatalf("secret leaked:\n%s", s)
	}
	if strings.Contains(s, "✗ act") {
		t.Errorf("act version mismatch must only warn:\n%s", s)
	}
}

func TestDoctorUnresolvableKey(t *testing.T) {
	fakeTools(t, "0.2.89")
	cfg := filepath.Join(t.TempDir(), "config.yml")
	os.WriteFile(cfg, []byte("app:\n  id: 1\n  private_key: {env: HOMERUN_TEST_UNSET}\n"), 0o600)
	var out bytes.Buffer
	if err := Doctor(context.Background(), cfg, &out); err == nil || !strings.Contains(out.String(), "✗ GitHub App private key source resolves") {
		t.Fatalf("err=%v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "!") {
		t.Errorf("matching act version should not warn:\n%s", out.String())
	}
}

func TestSaveAppPrintsKeyWhenFileWriteFails(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "k.pem")
	os.Symlink(filepath.Join(dir, "elsewhere"), link)
	var out bytes.Buffer
	if err := saveApp(filepath.Join(dir, "config.yml"), link, 1, "s", "u", testPEM, &out); err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(out.String(), "BEGIN HOMERUN APP KEY OUTPUT-----\n"+testPEM) {
		t.Fatalf("key lost on write failure:\n%s", out.String())
	}
}

func TestCreateAppRejectsBadKeyFileBeforeExchange(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "k.pem")
	os.Symlink(filepath.Join(dir, "elsewhere"), link)
	for _, p := range []string{link, dir} {
		exchanged := false
		err := createApp(context.Background(), "", false, filepath.Join(dir, "c.yml"), p,
			func(string) error { t.Error("browser opened"); return nil }, io.Discard,
			func(context.Context, string) (*github.AppConfig, error) { exchanged = true; return nil, nil })
		if err == nil || exchanged {
			t.Errorf("%s: err=%v exchanged=%v", p, err, exchanged)
		}
	}
}

func TestHandlerHostAndSingleUse(t *testing.T) {
	codes := make(chan string, 2)
	srv := httptest.NewUnstartedServer(nil)
	srv.Config.Handler = onlyHost(srv.Listener.Addr().String(), handler("https://x", "{}", "s1", codes))
	srv.Start()
	defer srv.Close()
	get := func(host, path string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get("evil.example:80", "/"); c != 400 {
		t.Fatalf("rebinding host served: %d", c)
	}
	if c := get("", "/"); c != 200 {
		t.Fatalf("own host rejected: %d", c)
	}
	if c := get("", "/callback?state=s1&code=a"); c != 200 {
		t.Fatalf("first callback: %d", c)
	}
	if c := get("", "/callback?state=s1&code=b"); c != 400 {
		t.Fatalf("replayed callback accepted: %d", c)
	}
	if got := <-codes; got != "a" || len(codes) != 0 {
		t.Fatalf("codes: %q, %d left", got, len(codes))
	}
}

func TestUnitQuotesPathWithSpace(t *testing.T) {
	u := Unit("/x", "/c", `/my bin:/a"b`)
	if !strings.Contains(u, `Environment="PATH=/my bin:/a\"b"`) {
		t.Fatalf("PATH not quoted:\n%s", u)
	}
}

func TestDoctorHidesConfigParseError(t *testing.T) {
	fakeTools(t, "0.2.89")
	cfg := filepath.Join(t.TempDir(), "config.yml")
	os.WriteFile(cfg, []byte("app: [LEAKYVALUE\n"), 0o600)
	var out bytes.Buffer
	if err := Doctor(context.Background(), cfg, &out); err == nil || strings.Contains(out.String(), "LEAKYVALUE") || !strings.Contains(out.String(), "cannot parse") {
		t.Fatalf("err=%v\n%s", err, out.String())
	}
}

func TestDoctorShowsSecretCommandError(t *testing.T) {
	fakeTools(t, "0.2.89")
	cfg := filepath.Join(t.TempDir(), "config.yml")
	os.WriteFile(cfg, []byte("app:\n  id: 1\n  private_key: {cmd: \"echo vault locked >&2; exit 1\"}\n"), 0o600)
	var out bytes.Buffer
	Doctor(context.Background(), cfg, &out)
	if !strings.Contains(out.String(), "command failed: exit status 1: vault locked") {
		t.Fatalf("detail missing:\n%s", out.String())
	}
}

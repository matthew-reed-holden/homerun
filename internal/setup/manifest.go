package setup

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/matthew-reed-holden/homerun/internal/config"
)

func manifest(name, redirect string, public bool) map[string]any {
	return map[string]any{
		"name":         name,
		"url":          "https://github.com/matthew-reed-holden/homerun",
		"redirect_url": redirect,
		"public":       public,
		"default_permissions": map[string]string{
			"checks": "write", "contents": "read", "pull_requests": "read", "metadata": "read",
		},
		"default_events": []string{},
	}
}

var nonName = regexp.MustCompile(`[^a-z0-9-]+`)

// appName must be unique across GitHub and at most 34 characters.
func appName(host string) string {
	h := strings.Trim(nonName.ReplaceAllString(strings.ToLower(host), "-"), "-")
	if len(h) > 20 {
		h = strings.TrimRight(h[:20], "-")
	}
	if h == "" {
		return "homerun-" + randHex(2)
	}
	return "homerun-" + h + "-" + randHex(2)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

var page = template.Must(template.New("p").Parse(`<!doctype html><meta charset="utf-8"><title>homerun setup</title>
<form id="f" method="post" action="{{.Action}}"><input type="hidden" name="manifest" value="{{.Manifest}}"></form>
<p>Sending you to GitHub to create the homerun App…</p>
<script>document.getElementById("f").submit()</script>`))

func handler(action, manifestJSON, state string, codes chan<- string) http.Handler {
	var used atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page.Execute(w, map[string]any{"Action": template.URL(action), "Manifest": manifestJSON})
	})
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 || q.Get("code") == "" {
			http.Error(w, "unexpected callback: state mismatch or missing code", http.StatusBadRequest)
			return
		}
		// A replayed callback must not queue a second code for the same App.
		if !used.CompareAndSwap(false, true) {
			http.Error(w, "this callback was already used", http.StatusBadRequest)
			return
		}
		codes <- q.Get("code")
		io.WriteString(w, "GitHub App created. You can close this tab and return to your terminal.")
	})
	return mux
}

// onlyHost rejects any request whose Host is not the listener's own address.
// Otherwise a DNS-rebinding page could read "/", which embeds the state.
func onlyHost(host string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "unexpected host", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CreateApp runs GitHub's App manifest flow: a local page posts the manifest to
// GitHub, GitHub redirects back with a code, and the code is exchanged for the
// App's id and private key. With keyFile empty the key is only printed, so it
// reaches disk only if the user chose a file.
func CreateApp(ctx context.Context, org string, public bool, cfgPath, keyFile string, open func(string) error, out io.Writer) error {
	gc, err := github.NewClient()
	if err != nil {
		return err
	}
	return createApp(ctx, org, public, cfgPath, keyFile, open, out, func(ctx context.Context, code string) (*github.AppConfig, error) {
		app, _, err := gc.Apps.CompleteAppManifest(ctx, code)
		return app, err
	})
}

// createApp takes the code exchange as a parameter so tests need no network.
func createApp(ctx context.Context, org string, public bool, cfgPath, keyFile string, open func(string) error, out io.Writer,
	exchange func(ctx context.Context, code string) (*github.AppConfig, error)) error {
	if keyFile != "" {
		// Checked before the App exists: a failure afterwards can only be rescued by printing the key.
		abs, err := filepath.Abs(keyFile)
		if err != nil {
			return err
		}
		keyFile = abs
		if err := checkKeyPath(keyFile); err != nil {
			return err
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	base := "http://" + l.Addr().String()
	state := randHex(16)
	host, _ := os.Hostname()
	m, err := json.Marshal(manifest(appName(host), base+"/callback", public))
	if err != nil {
		return err
	}
	action := "https://github.com/settings/apps/new?state=" + state
	if org != "" {
		action = "https://github.com/organizations/" + url.PathEscape(org) + "/settings/apps/new?state=" + state
	}
	codes := make(chan string, 1)
	// Without a header timeout a local client holding a connection open could pin the server.
	srv := &http.Server{Handler: onlyHost(l.Addr().String(), handler(action, string(m), state, codes)), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	defer srv.Close()

	fmt.Fprintf(out, "Opening %s/ to create the GitHub App...\n", base)
	if err := open(base + "/"); err != nil {
		fmt.Fprintf(out, "Could not open a browser. Open this URL yourself: %s/\n", base)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var code string
	select {
	case code = <-codes:
	case <-ctx.Done():
		return errors.New("timed out waiting for GitHub to redirect back")
	}
	app, err := exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchanging the manifest code: %w", err)
	}
	return saveApp(cfgPath, keyFile, app.GetID(), app.GetSlug(), app.GetHTMLURL(), app.GetPEM(), out)
}

func saveApp(cfgPath, keyFile string, id int64, slug, htmlURL, pem string, out io.Writer) error {
	if keyFile == "" {
		// Printed before the config write so a config failure cannot lose the only copy.
		printKey(out, slug, id, pem)
		if err := config.SetApp(cfgPath, id, ""); err != nil {
			return err
		}
		fmt.Fprintf(out, "Store the key above in a secret manager, then set app.private_key in %s to\n"+
			"{cmd: \"<a command that prints the key>\"}. homerun did not write it to disk.\n", cfgPath)
	} else {
		abs, err := filepath.Abs(keyFile)
		if err != nil {
			return err
		}
		if err := writeKeyFile(abs, []byte(pem)); err != nil {
			// The App already exists and this is the only copy of its key.
			printKey(out, slug, id, pem)
			return err
		}
		if err := config.SetApp(cfgPath, id, "cat "+shellQuote(abs)); err != nil {
			fmt.Fprintf(out, "The key was written to %s but the config update failed.\n", abs)
			return err
		}
		fmt.Fprintf(out, "Created GitHub App %s (id %d). Its private key is in %s (mode 600).\n", slug, id, abs)
	}
	fmt.Fprintf(out, `
Next:
  1. Install the App on your repos: %s/installations/new
  2. homerun repo add OWNER/NAME
  3. homerun daemon --install
`, htmlURL)
	return nil
}

func printKey(out io.Writer, slug string, id int64, pem string) {
	fmt.Fprintf(out, "Private key for GitHub App %s (id %d), shown once:\n-----BEGIN HOMERUN APP KEY OUTPUT-----\n%s\n-----END HOMERUN APP KEY OUTPUT-----\n",
		slug, id, strings.TrimRight(pem, "\n"))
}

// checkKeyPath rejects what writeKeyFile would later refuse, while the user can still fix it.
func checkKeyPath(path string) error {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to overwrite it", path)
		}
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory", path)
		}
	}
	return os.MkdirAll(filepath.Dir(path), 0o700)
}

// shellQuote closes, escapes and reopens the quoting around an embedded quote
// so a path cannot break out of the command that config runs through sh -c.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func writeKeyFile(path string, data []byte) error {
	// Rename would replace the link rather than follow it; refusing avoids silently
	// swapping something the user placed there.
	if err := checkKeyPath(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// CreateTemp opens 0600, so the key is never readable by others, even briefly.
	f, err := os.CreateTemp(dir, ".app-key-*.tmp")
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

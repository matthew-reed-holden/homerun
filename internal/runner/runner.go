package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/actlog"
	"github.com/matthew-reed-holden/homerun/internal/workflow"
)

type Runner struct {
	Act       string
	Platforms map[string]string
	CacheRoot string
}

type Spec struct {
	Dir       string
	Event     string
	EventPath string
	Workflow  string // path relative to Dir
	Secrets   map[string]string
	Slot      int
	Mask      func(string) string
	// Fork runs must not reach the host or the Docker socket.
	Fork bool
}

const (
	maxLine          = 1 << 20
	noToken          = "homerun-no-token"
	defaultActImage  = "catthehacker/ubuntu:act-latest"
	truncationMarker = " [truncated]"
)

var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

var envAllow = []string{"PATH", "HOME", "USER", "LANG", "DOCKER_HOST", "XDG_RUNTIME_DIR",
	"DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"}

// Env builds the child environment from an allowlist so homerun's own
// environment (tokens, config) never leaks into act or host commands.
func Env(secrets map[string]string) []string {
	var env []string
	for _, k := range envAllow {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		env = append(env, n+"="+secrets[n])
	}
	return env
}

func (r *Runner) Run(ctx context.Context, s Spec, onEvent func(actlog.Event)) error {
	if s.Mask == nil {
		s.Mask = func(v string) string { return v }
	}
	// act uppercases secret names and prompts/exits on missing or empty values.
	secrets := make(map[string]string, len(s.Secrets)+1)
	for n, v := range s.Secrets {
		if !secretName.MatchString(n) {
			return fmt.Errorf("invalid secret name %q", n)
		}
		for _, k := range envAllow {
			if n == k {
				return fmt.Errorf("secret name %q collides with a reserved environment variable", n)
			}
		}
		if v == "" {
			return fmt.Errorf("secret %q has an empty value", n)
		}
		secrets[n] = v
	}
	// Without GITHUB_TOKEN act runs `gh auth token` on the host and injects the user's own token.
	if _, ok := secrets["GITHUB_TOKEN"]; !ok {
		secrets["GITHUB_TOKEN"] = noToken
	}
	// act reads these from its working directory, which is the commit under test;
	// a commit must not be able to change act's flags, secrets or vars.
	if err := os.RemoveAll(filepath.Join(s.Dir, ".actrc")); err != nil {
		return err
	}
	// act prepends args from ~/.actrc and $XDG_CONFIG_HOME/act/actrc, and gh reads ~/.config/gh.
	home := filepath.Join(r.CacheRoot, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	env := append(Env(secrets), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	// Keep registry auth working despite the relocated HOME.
	if dc := os.Getenv("DOCKER_CONFIG"); dc != "" {
		env = append(env, "DOCKER_CONFIG="+dc)
	} else if h := os.Getenv("HOME"); h != "" {
		env = append(env, "DOCKER_CONFIG="+filepath.Join(h, ".docker"))
	}

	slot := fmt.Sprintf("slot-%d", s.Slot)
	if s.Fork {
		// A fork must not write caches that trusted runs read.
		slot = "fork-" + slot
	}
	cache := filepath.Join(r.CacheRoot, slot)
	args := []string{s.Event, "-W", s.Workflow, "-e", s.EventPath, "--json", "--rm",
		"--secret-file", os.DevNull, "--env-file", os.DevNull, "--var-file", os.DevNull, "--input-file", os.DevNull,
		// One cache per slot: concurrent act runs race on a shared action cache.
		"--action-cache-path", filepath.Join(cache, "actions"),
		"--cache-server-path", filepath.Join(cache, "cache")}
	if s.Fork {
		// act mounts the Docker socket into job containers by default, which is root-equivalent,
		// and defaults to host networking.
		args = append(args, "--container-daemon-socket", "-", "--network", "bridge", "--no-cache-server")
	} else {
		args = append(args, "-P", workflow.HostLabel+"=-self-hosted")
	}
	labels := make([]string, 0, len(r.Platforms))
	for l := range r.Platforms {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		img := r.Platforms[l]
		if s.Fork && img == "-self-hosted" {
			// Remapping (not dropping) keeps act off its interactive default-image survey.
			img = defaultActImage
		}
		args = append(args, "-P", l+"="+img)
	}
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// "-s NAME" makes act read the value from its environment, keeping it out of argv.
		args = append(args, "-s", n)
	}

	cmd := exec.CommandContext(ctx, r.Act, args...)
	cmd.Dir = s.Dir
	cmd.Env = env
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) } // act cleans up containers on SIGINT
	cmd.WaitDelay = 30 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	events := make(chan actlog.Event, 64)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		p := &actlog.Parser{Mask: s.Mask}
		readLines(stdout, func(l []byte) {
			for _, e := range p.Feed(l) {
				events <- e
			}
		})
	}()
	go func() {
		defer wg.Done()
		readLines(stderr, func(l []byte) {
			events <- actlog.Event{Kind: actlog.Line, Text: s.Mask(string(l))}
		})
	}()
	go func() { wg.Wait(); close(events) }()
	for e := range events {
		onEvent(e)
	}
	return cmd.Wait()
}

// readLines caps each line at maxLine but keeps reading: a Scanner stops at its
// limit, the pipe then stops draining, and act blocks.
func readLines(r io.Reader, f func([]byte)) {
	br := bufio.NewReader(r)
	var line []byte
	truncated := false
	for {
		chunk, err := br.ReadSlice('\n')
		if room := maxLine - len(line); room > 0 {
			line = append(line, chunk[:min(room, len(chunk))]...)
			truncated = truncated || len(chunk) > room
		} else {
			truncated = true
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if len(line) > 0 || truncated {
			l := bytes.TrimRight(line, "\r\n")
			if truncated {
				l = append(l[:len(l):len(l)], truncationMarker...)
			}
			f(l)
		}
		line, truncated = line[:0], false
		if err != nil {
			return
		}
	}
}

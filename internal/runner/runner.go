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

	// extraEnv lets tests feed the stub act through the otherwise explicit environment.
	extraEnv []string
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

// Env builds the child environment from an allowlist so homerun's own
// environment (tokens, config) never leaks into act or host commands.
func Env(secrets map[string]string) []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "USER", "LANG", "DOCKER_HOST", "XDG_RUNTIME_DIR"} {
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
	// act reads these from its working directory, which is the commit under test;
	// a commit must not be able to change act's flags, secrets or vars.
	if err := os.Remove(filepath.Join(s.Dir, ".actrc")); err != nil && !os.IsNotExist(err) {
		return err
	}
	cache := filepath.Join(r.CacheRoot, fmt.Sprintf("slot-%d", s.Slot))
	args := []string{s.Event, "-W", s.Workflow, "-e", s.EventPath, "--json", "--rm",
		"--secret-file", os.DevNull, "--env-file", os.DevNull, "--var-file", os.DevNull, "--input-file", os.DevNull,
		// One cache per slot: concurrent act runs race on a shared action cache.
		"--action-cache-path", filepath.Join(cache, "actions"),
		"--cache-server-path", filepath.Join(cache, "cache")}
	if s.Fork {
		// act mounts the Docker socket into job containers by default, which is root-equivalent.
		args = append(args, "--container-daemon-socket", "-")
	} else {
		args = append(args, "-P", workflow.HostLabel+"=-self-hosted")
	}
	labels := make([]string, 0, len(r.Platforms))
	for l := range r.Platforms {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		if s.Fork && r.Platforms[l] == "-self-hosted" {
			continue
		}
		args = append(args, "-P", l+"="+r.Platforms[l])
	}
	names := make([]string, 0, len(s.Secrets))
	for n := range s.Secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// "-s NAME" makes act read the value from its environment, keeping it out of argv.
		args = append(args, "-s", n)
	}

	cmd := exec.CommandContext(ctx, r.Act, args...)
	cmd.Dir = s.Dir
	cmd.Env = append(Env(s.Secrets), r.extraEnv...)
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

// readLines has no line-length cap: a Scanner stops at its limit, the pipe
// then stops draining, and act blocks.
func readLines(r io.Reader, f func([]byte)) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			f(bytes.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return
		}
	}
}

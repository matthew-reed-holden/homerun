package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/gh"
)

const minAct = "0.2.89" // the version homerun's act integration was verified against

// Doctor never prints a resolved secret or an error that could embed one.
func Doctor(ctx context.Context, cfgPath string, out io.Writer) error {
	ok := true
	check := func(name string, err error) {
		if err != nil {
			ok = false
			fmt.Fprintf(out, "✗ %s: %v\n", name, err)
			return
		}
		fmt.Fprintf(out, "✓ %s\n", name)
	}
	for _, bin := range []string{"git", "docker", "act"} {
		_, err := exec.LookPath(bin)
		check(bin+" on PATH", err)
		if err != nil {
			continue
		}
		switch bin {
		case "docker":
			check("docker daemon reachable", exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run())
		case "act":
			// Another version may work but was not verified, so it is a warning, not a failure.
			if v, err := actVersion(ctx); err != nil {
				check("act --version", err)
			} else if !versionAtLeast(v, minAct) || !versionAtLeast(minAct, v) {
				fmt.Fprintf(out, "! act %s found; homerun was verified against %s\n", v, minAct)
			}
		}
	}
	cfg, warns, err := config.LoadUser(cfgPath)
	check("config "+cfgPath, err)
	for _, w := range warns {
		fmt.Fprintf(out, "! %s\n", w)
	}
	if err == nil {
		checkConfig(ctx, cfgPath, cfg, check)
	}
	if !ok {
		return errors.New("some checks failed")
	}
	return nil
}

func checkConfig(ctx context.Context, cfgPath string, cfg *config.User, check func(string, error)) {
	if fi, err := os.Stat(cfgPath); err == nil {
		var perr error
		if m := fi.Mode().Perm(); m != 0o600 {
			perr = fmt.Errorf("mode is %o, want 600", m)
		}
		check("config file mode 600", perr)
	}
	if cfg.App.ID == 0 {
		check("GitHub App id set", errors.New("app.id is missing; run homerun setup"))
		return
	}
	key, err := cfg.App.PrivateKey.Resolve(ctx)
	check("GitHub App private key source resolves", err)
	if err != nil {
		return
	}
	c, err := gh.New(cfg.App.ID, []byte(key))
	if err != nil {
		// The library's parse error is dropped in case it quotes key material.
		check("GitHub App key valid", errors.New("not a valid RSA private key"))
		return
	}
	check("GitHub App key valid", nil)
	for repo := range cfg.Repos {
		_, err := c.DefaultBranch(ctx, repo)
		check("App installed on "+repo, err)
	}
}

func actVersion(ctx context.Context) (string, error) {
	b, err := exec.CommandContext(ctx, "act", "--version").Output()
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b)) // "act version 0.2.89"
	if len(f) == 0 {
		return "", errors.New("no version output")
	}
	return f[len(f)-1], nil
}

func versionAtLeast(v, min string) bool {
	a, b := strings.Split(v, "."), strings.Split(min, ".")
	for i := range b {
		if i >= len(a) {
			return false
		}
		x, err1 := strconv.Atoi(a[i])
		y, err2 := strconv.Atoi(b[i])
		if err1 != nil || err2 != nil {
			return false
		}
		if x != y {
			return x > y
		}
	}
	return true
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/matthew-reed-holden/homerun/internal/api"
	"github.com/matthew-reed-holden/homerun/internal/config"
	"github.com/matthew-reed-holden/homerun/internal/daemon"
	"github.com/matthew-reed-holden/homerun/internal/gh"
	"github.com/matthew-reed-holden/homerun/internal/setup"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
	"github.com/matthew-reed-holden/homerun/internal/tui"
)

const usage = `usage:
  homerun                                   open the dashboard
  homerun daemon [--config F] [--install]   run the daemon, or install it as a systemd user service
  homerun setup [--org ORG] [--public] [--key-file PATH]
                                            create the homerun GitHub App. Without --key-file the
                                            private key is printed once, for a secret manager;
                                            with it the key is written to PATH (mode 0600)
  homerun repo add OWNER/NAME               register a repo
  homerun run [--pr N] OWNER/NAME [BRANCH]  run a branch head or PR now. A fork PR is held until
                                            approved; running it with --pr N is the approval
  homerun doctor                            check the environment
`

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 0:
		err = tui.Run(api.NewClient(api.SocketPath()))
	case args[0] == "daemon":
		err = cmdDaemon(args[1:])
	case args[0] == "setup":
		err = cmdSetup(args[1:])
	case args[0] == "repo" && len(args) > 1 && args[1] == "add":
		err = cmdRepoAdd(args[2:])
	case args[0] == "run":
		err = cmdRun(args[1:])
	case args[0] == "doctor":
		err = cmdDoctor(args[1:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "homerun:", err)
		os.Exit(1)
	}
}

func flags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fs, fs.String("config", config.DefaultUserPath(), "config file")
}

func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "homerun")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "homerun")
}

func loadClient(ctx context.Context, path string) (*config.User, *gh.Client, error) {
	cfg, warns, err := config.LoadUser(path)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range warns {
		slog.Warn(w)
	}
	if cfg.App.ID == 0 {
		return nil, nil, errors.New("no GitHub App configured; run: homerun setup")
	}
	key, err := cfg.App.PrivateKey.Resolve(ctx)
	if err != nil {
		if d := config.Detail(err); d != "" {
			return nil, nil, fmt.Errorf("app.private_key: %w: %s", err, d)
		}
		return nil, nil, fmt.Errorf("app.private_key: %w", err)
	}
	c, err := gh.New(cfg.App.ID, []byte(key))
	return cfg, c, err
}

func cmdDaemon(args []string) error {
	fs, cfgPath := flags("daemon")
	install := fs.Bool("install", false, "install and start a systemd user service")
	fs.Parse(args)
	if *install {
		return setup.InstallSystemd(*cfgPath, os.Stdout)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, client, err := loadClient(ctx, *cfgPath)
	if err != nil {
		return err
	}
	act, err := exec.LookPath("act")
	if err != nil {
		return errors.New("act not found on PATH (see: homerun doctor)")
	}
	// New takes the state-dir lock, so a second daemon fails here before it can touch the socket.
	d, err := daemon.New(daemon.Options{Config: cfg, ConfigPath: *cfgPath, Source: client, Reporter: client,
		AppID: client.AppID(), StateDir: stateDir(), Act: act})
	if err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() {
		err := daemon.Serve(ctx, api.SocketPath(), d.Handler())
		if err != nil {
			stop()
		}
		serveErr <- err
	}()
	slog.Info("homerun daemon started", "repos", len(cfg.Repos), "socket", api.SocketPath())
	runErr := d.Run(ctx)
	stop()
	// A dead API server leaves a daemon nobody can reach, so its error wins over a clean shutdown.
	if err := <-serveErr; err != nil {
		return fmt.Errorf("api server: %w", err)
	}
	return runErr
}

func cmdSetup(args []string) error {
	fs, cfgPath := flags("setup")
	org := fs.String("org", "", "create the App under this organization")
	public := fs.Bool("public", false, "allow installing the App on accounts other than its owner")
	keyFile := fs.String("key-file", "", "write the private key to this file (mode 0600); default prints it once")
	fs.Parse(args)
	open := func(u string) error { return exec.Command("xdg-open", u).Start() }
	return setup.CreateApp(context.Background(), *org, *public, *cfgPath, *keyFile, open, os.Stdout)
}

func cmdRepoAdd(args []string) error {
	fs, cfgPath := flags("repo add")
	fs.Parse(args)
	// The flag package stops at the first positional, so a trailing flag lands here.
	if fs.NArg() != 1 || !config.ValidRepo(fs.Arg(0)) {
		return errors.New("usage: homerun repo add [--config F] OWNER/NAME (flags go before the repo)")
	}
	name := fs.Arg(0)
	ctx := context.Background()
	_, client, err := loadClient(ctx, *cfgPath)
	if err != nil {
		return err
	}
	if _, err := client.DefaultBranch(ctx, name); err != nil {
		return fmt.Errorf("%w\nInstall the homerun App on %s, then try again", err, name)
	}
	if err := config.AddRepo(*cfgPath, name); err != nil {
		return err
	}
	fmt.Printf("Added %s.\n", name)
	switch err := api.NewClient(api.SocketPath()).Reload(ctx); {
	case err == nil:
		fmt.Println("The daemon reloaded its config.")
	case errors.Is(err, api.ErrUnreachable):
		fmt.Println("The daemon is not running; it will pick the repo up when it starts.")
	default:
		fmt.Println("The daemon did not reload its config:", err)
	}
	fmt.Printf("Then: require homerun's checks in %s's branch protection, and stop hosted Actions running on push and pull_request.\n", name)
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	pr := fs.Int("pr", 0, "pull request number")
	fs.Parse(args)
	if fs.NArg() < 1 || fs.NArg() > 2 || !config.ValidRepo(fs.Arg(0)) {
		return errors.New("usage: homerun run [--pr N] OWNER/NAME [BRANCH] (flags go before the repo)")
	}
	ctx := context.Background()
	c := api.NewClient(api.SocketPath())
	resp, err := c.Start(ctx, api.StartRequest{Repo: fs.Arg(0), Ref: fs.Arg(1), PR: *pr})
	if err != nil {
		return err
	}
	if resp.RunID == "" {
		fmt.Println(resp.Message)
		return nil
	}
	fmt.Println(startLine(resp))
	if err := streamLog(ctx, c, resp.RunID); err != nil {
		return err
	}
	run, err := waitRun(ctx, c, resp.RunID)
	if err != nil {
		return err
	}
	msg, err := summarize(run)
	fmt.Println(msg)
	return err
}

// startLine prefers the daemon's message, which says when the run is one that
// was already going.
func startLine(resp *api.StartResponse) string {
	if resp.Message != "" {
		return resp.Message
	}
	return "Queued run " + resp.RunID + "."
}

const pollEvery = time.Second

// streamLog follows the run's log, waiting while the run is queued (it has
// no log until it starts). A run that ends without ever starting has none.
func streamLog(ctx context.Context, c *api.Client, id string) error {
	waiting := false
	for {
		body, err := c.Log(ctx, id, true)
		var se *api.StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			run, err := c.Run(ctx, id)
			if err != nil {
				return err
			}
			if run.Done() {
				return nil
			}
			if !waiting {
				fmt.Println("Waiting for a free slot…")
				waiting = true
			}
			time.Sleep(pollEvery)
			continue
		}
		if err != nil {
			return err
		}
		// The log carries output from the commit under test.
		err = termsafe.Copy(os.Stdout, body)
		body.Close()
		return err
	}
}

// waitRun polls until the run will not change again: the log stream can end
// before the last check is reported.
func waitRun(ctx context.Context, c *api.Client, id string) (*api.Run, error) {
	for {
		run, err := c.Run(ctx, id)
		if err != nil || run.Done() {
			return run, err
		}
		time.Sleep(pollEvery)
	}
}

// summarize decides the exit status from the run's own checks.
func summarize(run *api.Run) (string, error) {
	var passed, failed int
	for _, ck := range run.Checks {
		switch ck.Conclusion {
		case "success", "skipped", "neutral":
			passed++
		default:
			failed++
		}
	}
	msg := fmt.Sprintf("run %s: %s, %d passed, %d failed", run.ID, run.Status, passed, failed)
	switch {
	case run.Status != "done":
		return msg, fmt.Errorf("run %s did not finish (%s)", run.ID, run.Status)
	case passed+failed == 0:
		return msg, fmt.Errorf("run %s reported no checks", run.ID)
	case failed > 0:
		return msg, fmt.Errorf("run %s did not pass", run.ID)
	}
	return msg, nil
}

func cmdDoctor(args []string) error {
	fs, cfgPath := flags("doctor")
	fs.Parse(args)
	return setup.Doctor(context.Background(), *cfgPath, os.Stdout)
}

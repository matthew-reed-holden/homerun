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
	"github.com/matthew-reed-holden/homerun/internal/git"
	"github.com/matthew-reed-holden/homerun/internal/setup"
	"github.com/matthew-reed-holden/homerun/internal/termsafe"
	"github.com/matthew-reed-holden/homerun/internal/tui"
)

const usage = `usage:
  homerun                                   open the dashboard (runs checks while open, unless a daemon is running)
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
		err = cmdTUI()
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

// loadClient's detail flag adds a failed secret command's stderr to the error;
// the daemon passes false because its errors land in the journal on disk.
func loadClient(ctx context.Context, path string, detail bool) (*config.User, *gh.Client, error) {
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
		if d := config.Detail(err); detail && d != "" {
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
	cfg, client, err := loadClient(ctx, *cfgPath, false)
	if err != nil {
		return err
	}
	act, err := exec.LookPath("act")
	if err != nil {
		return errors.New("act not found on PATH (see: homerun doctor)")
	}
	// New takes the state-dir lock, so a second daemon fails here before it can touch the socket.
	d, err := daemon.New(daemon.Options{Config: cfg, ConfigPath: *cfgPath, Source: client, Reporter: client,
		AppID: client.AppID(), StateDir: stateDir(), Act: act, Owner: "daemon"})
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

// cmdTUI opens the dashboard, hosting the engine while it is open when no
// other process serves the API.
func cmdTUI() error {
	// The TUI owns the terminal, so the engine logs to a file meanwhile.
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(stateDir(), "homerun.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	slog.SetDefault(slog.New(slog.NewTextHandler(logf, nil)))
	e := &engine{cfgPath: config.DefaultUserPath(), owner: "tui", poll: true}
	_, hostErr := e.tryHost()
	cwd, _ := os.Getwd()
	cfgPath := config.DefaultUserPath()
	err = tui.Run(api.NewClient(api.SocketPath()), tui.Options{Host: e.tryHost, Hosting: e.hosting, HostErr: hostErr,
		Launch:  git.OriginRepo(context.Background(), cwd),
		AddRepo: func(ctx context.Context, n string) error { return addRepo(ctx, cfgPath, n) }})
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return errors.Join(err, e.shutdown())
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
	reload, err := addRepoReload(context.Background(), *cfgPath, name)
	if err != nil {
		return err
	}
	fmt.Printf("Added %s.\n", name)
	switch {
	case reload == nil:
		fmt.Println("The daemon reloaded its config.")
	case errors.Is(reload, api.ErrUnreachable):
		fmt.Println("The daemon is not running; it will pick the repo up when it starts.")
	default:
		fmt.Println("The daemon did not reload its config:", reload)
	}
	fmt.Printf("Then: require homerun's checks in %s's branch protection, and stop hosted Actions running on push and pull_request.\n", name)
	return nil
}

// addRepo checks the App can see name, registers it, and asks a running engine
// to reload. A reload that fails is not an error: the repo is registered.
func addRepo(ctx context.Context, cfgPath, name string) error {
	_, err := addRepoReload(ctx, cfgPath, name)
	return err
}

func addRepoReload(ctx context.Context, cfgPath, name string) (reload error, err error) {
	_, client, err := loadClient(ctx, cfgPath, true)
	if err != nil {
		return nil, err
	}
	if _, err := client.DefaultBranch(ctx, name); err != nil {
		if u, uerr := client.AppURL(ctx); uerr == nil && u != "" {
			return nil, fmt.Errorf("the homerun App is not installed on %s: install it at %s/installations/new", name, u)
		}
		return nil, fmt.Errorf("%w\nInstall the homerun App on %s, then try again", err, name)
	}
	if err := config.AddRepo(cfgPath, name); err != nil {
		return nil, err
	}
	return api.NewClient(api.SocketPath()).Reload(ctx), nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	pr := fs.Int("pr", 0, "pull request number")
	fs.Parse(args)
	if fs.NArg() < 1 || fs.NArg() > 2 || !config.ValidRepo(fs.Arg(0)) {
		return errors.New("usage: homerun run [--pr N] OWNER/NAME [BRANCH] (flags go before the repo)")
	}
	e := &engine{cfgPath: config.DefaultUserPath(), owner: "run", poll: false}
	if _, err := e.tryHost(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := runOnce(ctx, e, api.NewClient(api.SocketPath()), api.StartRequest{Repo: fs.Arg(0), Ref: fs.Arg(1), PR: *pr})
	return errors.Join(err, e.shutdown())
}

// runOnce starts a run and follows it to its end. On a signal, a hosting
// engine's shutdown cancels the run and closes its checks.
func runOnce(ctx context.Context, e *engine, c *api.Client, req api.StartRequest) error {
	resp, err := c.Start(ctx, req)
	if err != nil {
		return err
	}
	if resp.RunID == "" {
		fmt.Println(resp.Message)
		return nil
	}
	fmt.Println(startLine(resp))
	err = streamLog(ctx, c, resp.RunID)
	var run *api.Run
	if err == nil {
		run, err = waitRun(ctx, c, resp.RunID)
	}
	if ctx.Err() != nil {
		if e.hosting() {
			// The engine and its socket go away with this process.
			if err := e.shutdown(); err != nil {
				return err
			}
			return fmt.Errorf("run %s cancelled: homerun was closed", resp.RunID)
		}
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		run, err = waitRun(wctx, c, resp.RunID)
	}
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

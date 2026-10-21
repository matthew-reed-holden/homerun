# homerun

## What it is

homerun is local CI of record for GitHub repos. A daemon on your own machine watches registered repos for pushes and pull requests, runs their existing `.github/workflows/*.yml` under [act](https://github.com/nektos/act) against the exact commit, and reports each job back to GitHub as a check run. GitHub keeps showing the results: checks, branch protection, the PR page. A terminal UI shows PRs, checks and live logs across repos. homerun is not a pre-push previewer; for that, use act or wrkflw.

## Requirements

- Linux
- `git`, `docker` and `act` 0.2.89 (other versions work but `homerun doctor` warns) on `PATH`

`homerun doctor` checks all of them, including the act version.

## Install

```
go install github.com/matthew-reed-holden/homerun/cmd/homerun@latest
```

Or download a release tarball (linux amd64 or arm64) and put `homerun` on your `PATH`.

## Quick start

1. `homerun setup [--org ORG] [--public] [--key-file PATH]` creates the GitHub App. A private App can only be installed on the account that owns it, so pass `--org ORG` to create it under an organization, or `--public` to install one App across several owners. By default the private key is printed once, for a secret manager; `--key-file PATH` writes it to a mode-600 file instead.
2. Install the App on your repos.
3. `homerun repo add OWNER/NAME`
4. `homerun daemon --install` writes and enables a systemd user unit.
5. `homerun` opens the TUI.
6. In branch protection, require `homerun / <workflow> / <job>`.
7. Stop hosted runs: remove `push` and `pull_request` from `on:`, or disable Actions. When this machine will be off, see "When this machine is off".

To run a commit by hand: `homerun run [--pr N] OWNER/NAME [BRANCH]`. It exits non-zero if the run fails.

### TUI

`homerun` opens a lazygit-style view: **1** repos, **2** branches and PRs, **3** recent runs, and a main pane that follows the selection.

| Keys | |
|---|---|
| `1` `2` `3`, `tab`/`shift+tab` | focus a panel / cycle panes |
| `j`/`k`, `g`/`G`, mouse | move, top/bottom, click and scroll |
| `l`/`enter`, `h` | into the main pane / back to the panels |
| `space` | fold or unfold a job |
| `enter` on a job or step | open its log at that step |
| in a log: `/` `n` `N` `e` `f` `+` `esc` | search, next/prev match, next failure, follow, zoom, back |
| `r` `c` `o` `y` `ctrl+r` | rerun (forks ask first), cancel, open on GitHub, copy SHA, reload config |
| `R` | pick a repo (fuzzy search) |
| `?` `q` | help, quit |

## When this machine is off

Required checks stay pending while the daemon is down. Re-enabling hosted Actions is not enough: hosted Actions never produce the `homerun / ...` check names, so those required checks would still never complete. Also swap the required checks in branch protection for the hosted ones, or remove the `homerun / ...` ones temporarily. Restore them when the machine is back.

## Configuration

### User config: `~/.config/homerun/config.yml`

Owned by you and trusted. Keep it mode 600.

```yaml
app:
  id: 123456
  private_key: {cmd: "op read op://homerun/app/private-key"}
max_parallel: 1        # global cap
poll_interval: 30s
retain: 14d
repos:
  owner/name:
    secrets:
      GCP_SA_KEY: {cmd: "op read op://ci/gcp-key"}   # preferred
      NPM_TOKEN:  {env: NPM_TOKEN}                   # from the daemon's environment
      DB_PASS:    hunter2                            # allowed, with a warning
```

A secret takes one of three forms:

- `{cmd: "..."}`: the command's trimmed stdout. Preferred.
- `{env: NAME}`: read from the daemon's environment. The daemon does not see your shell, so set it where the daemon starts, for example `Environment=` in the systemd unit or a drop-in.
- A plain string. Allowed, but homerun warns.

homerun also warns when the file is readable by group or others.

### Repo config: `.github/homerun.yml` (optional, committed)

```yaml
host_jobs: [docker]      # run these jobs on the host (act -self-hosted)
max_parallel: 1          # per-repo cap
secrets: [GCP_SA_KEY]    # names only; values come from the user config
checks:                  # extra host commands run against the snapshot
  terraform-validate: terraform init -backend=false && terraform validate
```

With no repo config, every `push` and `pull_request` workflow runs under act with default settings and no secrets. Extra checks are named `homerun / checks / <name>`.

## Security model

- Repo config names secrets but never says how to get them. A commit cannot make homerun run a command to fetch one.
- Pull requests from forks (including ones whose head repo was deleted) are held until you approve them. They get a neutral `homerun` check, "fork PR: awaiting approval". Approve with `homerun run --pr N OWNER/NAME`.
- An approved fork run gets no secrets, and its `host_jobs` and `checks:` report neutral "skipped: fork PR". It gets no Docker socket and bridge networking. Jobs with privileged containers, volumes or ports, and unsafe reusable workflows, are skipped.
- `.actrc`, `.secrets`, `.env`, `.vars` and `.input` from the commit are ignored.
- Child processes get an explicit environment: a small allowlist from the daemon's environment, including PATH, HOME, USER, LANG, DOCKER_HOST and XDG_RUNTIME_DIR, plus the secrets the repo names. act runs with a private HOME and XDG_CONFIG_HOME, so your `~/.actrc` and `~/.config/act` are not read, and it never sees your gh token; DOCKER_CONFIG points at your real Docker config. homerun passes `GITHUB_TOKEN` as a placeholder unless the repo names a `GITHUB_TOKEN` secret.
- Secrets are masked in logs and check output.
- Host jobs run with your user's privileges, so only list jobs you trust.

## How it differs from GitHub

- PRs test the head commit, not the merge commit.
- Only `push` and `pull_request` trigger runs. Other workflows are listed in the TUI as "not handled locally".
- Job names containing expressions use the job id.
- The PR action is always `synchronize`.
- The "Re-run" button on github.com does nothing yet (v1.1). Use `r` in the TUI or `homerun run`.

## Roadmap

- **v1.1:** the "Re-run" button on github.com, via `check_suite` and `check_run` `rerequested` webhooks through a relay (smee.io or a tunnel).
- **v2:** built-in secret providers (`op:`, `gcp:`, ...); `schedule`, `workflow_dispatch` and `workflow_run` triggers; CD (GitHub Deployments, build once, promote by digest, rollback); several machines sharing work by claiming check runs.

## License

MIT

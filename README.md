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
3. `homerun repo add OWNER/NAME`. Adding needs no App: PRs show through your own token, and checks start once the App is installed.
4. `homerun daemon --install` writes and enables a systemd user unit.
5. `homerun` opens the TUI.
6. In branch protection, require `homerun / <workflow> / <job>`.
7. Stop hosted runs: remove `push` and `pull_request` from `on:`, or disable Actions. When this machine will be off, see "When this machine is off".

To run a commit by hand: `homerun run [--pr N] OWNER/NAME [BRANCH]`. It exits non-zero if the run fails.

### TUI

`homerun` opens a row of tabs for the picked repo: the PR search tabs, then **Branches** and **Runs**. Each tab is a table with a preview beside it that follows the selection. A PR's preview has two tabs, **Overview** and **Checks**. PR tabs use your own GitHub token (`gh auth token`, or `user_token` in the user config); without one, only Branches and Runs work.

| Keys | |
|---|---|
| `[`/`]`, `1`–`9` | previous/next tab, jump to a tab |
| `/` | filter the PR tab with a GitHub search query (`enter` apply, `esc` keep the old one, `ctrl+u` clear) |
| `R` | pick a repo (fuzzy search) |
| `j`/`k`, `g`/`G`, mouse | move, top/bottom, click and scroll |
| `l`/`enter`, `h`/`esc`, `tab` | into the preview / back to the table / switch between them |
| in a PR's preview: `[`/`]` | Overview or Checks |
| `m` `u`, in a PR's preview `c` | merge, update the branch, comment |
| `space` | fold or unfold a job |
| `enter` on a job or step | open its log at that step |
| in a log: `/` `n` `N` `e` `f` `+` `esc` | search, next/prev match, next failure, follow, zoom, back |
| `r` `x` `o` `y` `ctrl+r` | rerun (forks ask first), cancel, open on GitHub, copy SHA, reload config |
| `?` `q` | help, quit |

Icons need a Nerd Font. If they show as boxes, set `icons: unicode` (or `ascii`) in the user config; `nerd` is the default, and `?` names the setting.

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

`tabs:` (optional) replaces the PR search tabs: at most 5 entries, each with a `name` and a `query`. homerun adds `is:pr repo:<picked repo>` to the query. A bad entry fails config loading. Without the key you get these:

```yaml
tabs:
  - {name: My PRs, query: "is:open author:@me"}
  - {name: Review requested, query: "is:open review-requested:@me"}
  - {name: All open, query: "is:open"}
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

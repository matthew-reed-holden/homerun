# homerun: design

Date: 2026-10-12
Status: approved; amended 2026-10-12 with decisions from plan review (fork approval, child environment, fallback, claim unit)

## Purpose

homerun is local CI of record for GitHub repos. A daemon on your own machine
watches registered repos for pushes and pull requests, runs their existing
`.github/workflows/*.yml` under [act](https://github.com/nektos/act) against the
exact commit, and reports each job back to GitHub as a check run. A terminal UI
shows PRs, checks and live logs across repos, the way github.com does.

Why: GitHub-hosted Actions minutes cost money, and when an account's payment
fails, every job fails to start. homerun removes both: CI runs on hardware you
already own, and GitHub keeps showing the truth (checks, branch protection).

What it is not: a tool for previewing workflows before you push (act and
wrkflw cover that), or a CI server for a team's fleet. It works on any GitHub
repo, and the core knows nothing about any particular repo's stack.

## Decisions

| Topic | Decision |
|---|---|
| Engine | act, invoked as a subprocess. |
| Reporting | GitHub Checks API, through a GitHub App the user creates (`homerun setup`). |
| Relationship to hosted Actions | homerun is primary. Repos turn off hosted runs and require homerun's checks. When the machine is off, re-enable Actions and also swap the required checks in branch protection (or remove them temporarily), because hosted Actions never produce the `homerun / …` check names. The README documents this. |
| Source of truth | The repo's workflow YAML. Per-repo config only adds overrides and extra checks. |
| Coordination | GitHub check runs are the dedupe and claim record, so several machines sharing work (v2) needs no new design. |
| Language | Go, one static binary. Bubble Tea for the TUI. |
| License / home | MIT, personal GitHub account. |

## Architecture

One binary, three modes:

```
homerun daemon                           # long-running; systemd --user unit
homerun                                  # TUI; talks to the daemon over a unix socket
homerun setup | repo add | run <repo> <sha>   # CLI verbs over the same socket
```

Units inside the daemon:

| Unit | Responsibility | Depends on |
|---|---|---|
| poller | Lists each repo's open PRs and branch heads, using ETags (a 304 costs no rate limit). Emits `(repo, sha, event)` for new heads. | GitHub REST |
| claimer | Skips a `(sha, event)` that already has a homerun check run on that sha whose `external_id` is the event key (`push:refs/heads/<branch>` or `pull_request:<N>`); otherwise creates `queued` check runs. | reporter |
| scheduler | FIFO queue with a global cap and a per-repo cap. Cancels superseded PR runs only. A newer push to a branch does not cancel the older branch run. | |
| executor | Snapshots the sha with `git worktree add --detach` from a per-repo bare mirror. Writes the event payload, resolves secrets, and runs `act <event> --json` per workflow. Parses act's JSON log into per-job events. Runs `checks:` entries on the host. | act, git |
| reporter | Authenticates as the App (JWT, then installation token). Moves each job's check run through queued, in_progress and completed, with a conclusion and summary. | GitHub Checks API |
| store | `~/.local/state/homerun/runs/<id>/{meta.json,log}` plus a small state file holding the branch-head baselines. No database: GitHub keeps the durable history. | filesystem |
| api | HTTP over a unix socket, standard library only: state, log streaming, rerun, cancel. | |

act runs once per workflow, not per job, so `needs:` ordering is kept. Each job
(and each matrix leg) becomes its own check run.

The GitHub boundary sits behind two small interfaces (`Source` for reads and
`Reporter` for check runs), so daemon tests can use a fake.

## Data flow

1. **Poll.** Every `poll_interval` (default 30s) per repo: list open PRs and branches.
2. **Detect.**
   - A PR head with no homerun check run for `(sha, event)` becomes a `pull_request` event. The check run is one on that sha whose `external_id` is `pull_request:<N>`.
   - Branch pushes are keyed the same way, by a check run whose `external_id` is `push:refs/heads/<branch>`.
   - A branch head that moved becomes a `push` event.
   - On `repo add`, existing branch heads are recorded as a baseline and not run. Open PRs without homerun checks are run.
3. **Claim.** Create one `queued` check run per job the event would trigger.
   - The job list comes from `act <event> --list` against the snapshot, using the event payload.
   - Matrix legs aren't known until runtime, so each leg gets its own check run when it starts. The job's own check run stays open until the legs finish, then concludes with the worst leg result. It isn't closed as `skipped`, because GitHub treats `skipped` as passing for a required check.
4. **Schedule.** Wait for a slot under `max_parallel`, both global and per repo.
   - Only PR runs are superseded: a newer head on the same PR cancels the older run, and its check runs are marked `cancelled`.
   - Branch pushes are not cancelled by a newer push. Each one runs.
5. **Execute.**
   - Update the bare mirror and fetch `refs/pull/N/head` for PRs. Snapshot the sha into a detached worktree.
   - Write the event JSON with realistic fields (base and head SHAs, refs, PR number, repository), so `github.event.*` expressions behave as they do on GitHub.
   - act ignores workflow-level `branches`, `tags` and `paths` filters, so homerun evaluates them itself (step 3) and passes act one matched workflow at a time with `-W`.
   - Resolve secrets and pass them to act through its environment (`-s NAME`). Never write them to disk.
   - Map `host_jobs` to act's `-self-hosted` platform.
   - Give each concurrency slot its own `--action-cache-path`, so parallel runs never share act's cache.
6. **Report.** Job start sets `in_progress`. Job end sets `completed` with a conclusion. The summary holds the failed step and the log tail, kept under GitHub's 65,535-character output limit.
7. **Clean up.** Remove the worktree. Keep the run directory, pruned by age (default 14 days).

**Triggers in v1:** `push` and `pull_request` only. Workflows that respond only to
other events (`schedule`, `workflow_dispatch`, `workflow_run`,
`pull_request_target`) are skipped and listed in the TUI as "not handled
locally".

**Check names:** `homerun / <workflow name> / <job name>`. Extra checks are named
`homerun / checks / <name>`. Repos put these names in their branch protection.

## Configuration

### User config: `~/.config/homerun/config.yml`

This file is owned by the user and holds everything trusted, including secret values or the commands that produce them.

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

A secret value takes one of three forms:
- `{cmd: "..."}`: the command's stdout, trimmed.
- `{env: NAME}`: read from the daemon's environment.
- A plain string.

homerun warns, but never refuses, when:
- a secret is stored as plaintext,
- the file is readable by group or others (it should be mode 600).

### Repo config: `.github/homerun.yml` (optional, committed)

```yaml
host_jobs: [docker]      # run these jobs on the host (act -self-hosted)
max_parallel: 1          # per-repo cap
secrets: [GCP_SA_KEY]    # names only; values come from the user config
checks:                  # extra host commands run against the snapshot
  terraform-validate: terraform init -backend=false && terraform validate
```

With no repo config, every `push` and `pull_request` workflow runs under act with
default settings and no secrets.

### Trust rules (on by default)

- The repo config can only *name* secrets. A commit can't make homerun run a command to fetch one.
- Pull requests from forks never receive secrets.
- `checks:` and `host_jobs` from a fork PR are not run on the host. They report `neutral` with the message "skipped: fork PR".
- A pull request from a fork (including one whose head repo was deleted) does not run automatically, because contributors from forks can run the workflow on their own machines.
  - homerun claims it with one check named `homerun`, conclusion `neutral`, summary "fork PR: awaiting approval — run `homerun run --pr N OWNER/NAME`".
  - The owner approves by running that command.
  - An approved fork run still gets no secrets. Its `host_jobs` and `checks:` report neutral "skipped: fork PR".
  - act runs with `--container-daemon-socket -` and with no host mapping, because act otherwise bind-mounts the Docker socket into job containers, which is root-equivalent.
- homerun builds each child process's environment explicitly (act and `checks:` commands): a small allowlist from the daemon's environment (PATH, HOME, USER, LANG, DOCKER_HOST, XDG_RUNTIME_DIR) plus only the secrets the repo names. Child processes never inherit the daemon's full environment.

## Findings from the act spike (act 0.2.89)

- **Unmasked secrets.** act masks secrets in `msg`, but its JSON `step` field carries the raw `run:` text. If a secret is interpolated there, it appears unmasked. homerun masks every secret value (and each line of a multi-line secret) in everything it stores or uploads.
- **Matrix results.** act puts the whole matrix job's result in every leg's `jobResult`. A matrix leg's conclusion therefore comes from that leg's own step results.
- **Host jobs.** `-P` maps `runs-on` labels, not jobs. homerun rewrites a host job's `runs-on` to `homerun-host` in the snapshot and passes `-P homerun-host=-self-hosted`.
- **Trigger filters.** act runs a workflow even when its `branches` or `paths` filter shouldn't match, so homerun filters workflows itself.
- **Per-directory config.** act reads `.actrc`, `.secrets`, `.env`, `.vars` and `.input` from the working directory, and in a snapshot those come from the commit under test. homerun deletes `.actrc` from the snapshot and passes `/dev/null` for the other four.
- **Interactive prompt.** act prompts for a default image on first use. homerun always passes `-P` for `ubuntu-latest`, `ubuntu-24.04` and `ubuntu-22.04` (default `catthehacker/ubuntu:act-latest`), overridable with `platforms:` in the user config.
- **PR commit.** PR runs test the PR's head commit, not GitHub's `refs/pull/N/merge` merge commit. The result is deterministic and always exists, even when the PR has conflicts.

## Error handling

| Case | Behavior |
|---|---|
| A job fails | `failure`. The summary shows the failed step and the last ~200 log lines. |
| homerun fails (Docker down, act missing, invalid config, a secret command exits non-zero) | `failure`, with a summary that starts `homerun error:`. Secret values are never logged; only the secret's name and the command's exit code are. |
| The daemon restarts mid-run | On startup, homerun's own check runs left `queued` or `in_progress` with no live process are closed as `cancelled` ("runner restarted"), then re-queued. |
| GitHub is unreachable or rate-limited | Retry with exponential backoff. Execution continues, and reports flush when GitHub comes back. |
| The machine is off | Required checks stay pending. The README tells users to re-enable hosted Actions. They must also swap the required checks in branch protection (or remove them temporarily), because hosted Actions never produce the `homerun / …` check names. |

## TUI

- **Layout.** The left pane lists registered repos with a status rollup. The right pane shows the selected repo's open PRs and branches, each expanding to its check runs. Those include homerun's checks, plus GitHub's own checks when present.
- **Actions.**
  - `enter`: tail the selected local run's log live.
  - `r`: rerun.
  - `c`: cancel.
  - `o`: open on github.com.
  - `q`: quit.
- **Data.** Daemon state comes over the unix socket. GitHub check runs come through the daemon, so it owns the token and the rate limit. The TUI is a client only.

## Setup and distribution

- `homerun setup` creates the GitHub App through the manifest flow.
  - It opens the browser and catches the redirect on a localhost callback.
  - It exchanges the code for the App's ID and private key, and writes the App ID to the user config.
  - It asks where to keep the key: print it for a secret manager, or write it to a mode-600 file.
  - The App's permissions: checks (write), contents (read), pull requests (read), metadata (read).
  - A private App can only be installed on the account that owns it. `--org ORG` creates it under an organization. `--public` lets one App be installed across several owners; only the key holder can act through it.
- `homerun repo add owner/name` checks that the App is installed on the repo, creates the bare mirror, and records branch baselines.
- `homerun daemon --install` writes and enables a systemd user unit.
- Releases are built with goreleaser (linux amd64 and arm64), plus `go install`.
- Runtime requirements: `git`, `docker`, and `act` on `PATH`. `homerun doctor` checks them, including the act version.

## Testing

- **Unit tests:**
  - config parsing, validation and warnings
  - event payload building
  - act JSON log parsing (fixtures recorded from real act runs)
  - scheduler caps, ordering and supersede-cancel
  - check-name generation, including matrix legs
- **Daemon tests:** poll, claim, run and report with the fake `Source` and `Reporter` and a stub act binary.
- **End-to-end test** (`make e2e`, manual and not in CI): a throwaway public test repo, a real App and real act, with a small workflow that has one passing job, one failing job and one matrix job.
- **Acceptance for v1:**
  1. theostack-backend's `ci.yml` passes under homerun on a real PR, with check runs visible on github.com and the `docker` job running on the host.
  2. The same for theostack-frontend's `ci.yml`.
  3. theostack-infra runs a `terraform-validate` check from `checks:` alone, without a workflow file.

## Roadmap

- **v1:** everything above.
- **v1.1:** the "Re-run" button on github.com, handling `check_suite` and `check_run` `rerequested` webhooks through a relay (smee.io or a tunnel).
- **v2:**
  - built-in secret providers (`op:`, `gcp:`, ...)
  - `schedule`, `workflow_dispatch` and `workflow_run` triggers
  - CD: GitHub Deployments, build once, promote by digest, rollback
  - several machines sharing work by claiming check runs, with stale-claim expiry

**Out of scope:** Windows and macOS runners (act runs Linux jobs only), GitLab,
and posting commit statuses (check runs replace them).

## First users

- `Lordhill-Digital/theostack-backend`: Go. `ci.yml` has `changes`, `checks`, `integration` (testcontainers), `docker` (buildx, set as a host job) and `secrets` (trufflehog).
- `Lordhill-Digital/theostack-frontend`: pnpm and Turborepo. `ci.yml` has `check`, `test`, `audit` and `e2e`. Tests must run with one worker because of memory.
- `Lordhill-Digital/theostack-infra`: Terraform with no workflows. Its checks come from `checks:` only.

Each repo keeps its existing scripts until homerun replaces them. Adopting homerun is a separate step per repo, not part of v1.

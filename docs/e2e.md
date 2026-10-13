# End-to-end release checklist

Run before each release. It uses real GitHub, a real App, real act and Docker.
File a bead for each failure (`bd create --type=bug`), fix it with a regression
test in the owning package, then re-run the step.

## 1. Throwaway repo

Create a public repo `matthew-reed-holden/homerun-e2e` with:

```yaml
# .github/workflows/ci.yml
name: CI
on: [push, pull_request]
jobs:
  pass: {runs-on: ubuntu-latest, steps: [{run: "echo ok"}]}
  fail: {runs-on: ubuntu-latest, steps: [{run: "echo bad; exit 1"}]}
  matrix:
    runs-on: ubuntu-latest
    strategy: {matrix: {n: [1, 2]}}
    steps: [{run: "echo leg ${{ matrix.n }}"}]
  host:
    runs-on: ubuntu-latest
    steps: [{run: "test ! -f /.dockerenv && echo on-host"}]
  secret: {runs-on: ubuntu-latest, steps: [{run: "echo ${{ secrets.E2E }}"}]}
  soft:
    runs-on: ubuntu-latest
    steps:
      - {run: "exit 1", continue-on-error: true}
      - {run: "echo after"}
  win: {runs-on: windows-latest, steps: [{run: "echo never"}]}
  call: {uses: ./.github/workflows/inner.yml}
```

```yaml
# .github/workflows/inner.yml
name: Inner
on: workflow_call
jobs:
  in: {runs-on: macos-latest, steps: [{run: "echo never"}]}
```

```yaml
# .github/homerun.yml
host_jobs: [host]
secrets: [E2E]
checks:
  shellcheck: "true"
```

Then:

1. `make build && ./bin/homerun doctor` (everything but the config passes).
2. `./bin/homerun setup`. Without `--key-file` it prints the private key once:
   store it in 1Password and set `app.private_key: {cmd: "op read op://..."}`
   in `~/.config/homerun/config.yml`. Install the App on the repo from the
   URL it prints.
3. Under `repos.matthew-reed-holden/homerun-e2e` in the user config add
   `secrets: {E2E: {cmd: "printf e2e-secret-value"}}`.
4. `./bin/homerun daemon` in one terminal, then
   `./bin/homerun repo add matthew-reed-holden/homerun-e2e`.

## 2. Verify on github.com

Push a commit, then open a PR from a branch. On both:

- [ ] `pass` ✔; `fail` ✘ titled "Failed at: …".
- [ ] `matrix (1)`, `matrix (2)` and the `matrix` parent ✔.
- [ ] `host` ✔ (it ran on the host).
- [ ] `secret` ✔; its output shows `***`, and `e2e-secret-value` appears
      nowhere on github.com or in `~/.local/state/homerun/runs/*/log`.
- [ ] `soft` ✔ (continue-on-error does not fail the job).
- [ ] `win` ✘ "runs-on label windows-latest is not mapped".
- [ ] `call` ✘ (its inner job's label is unmapped; never skipped).
- [ ] `checks / shellcheck` ✔.
- [ ] Push twice quickly to the PR: the first run's checks end `cancelled`.
- [ ] Kill the daemon mid-run and restart it: stranded checks end
      `cancelled` "runner restarted", and the run happens exactly once more.
- [ ] TUI (`./bin/homerun`): the repo and PR show; `enter` on a check streams
      its log; `r` reruns; `c` cancels; `o` opens github.com.
- [ ] `./bin/homerun run matthew-reed-holden/homerun-e2e main` waits and
      exits 1 (because `fail` fails).

## 3. Fork PR

From a second account, fork the repo and open a PR.

- [ ] Only a neutral `homerun` check "fork PR: awaiting approval" appears;
      nothing runs.
- [ ] In the TUI the PR row shows `[fork]`; `r` asks for confirmation and any
      key but `y` cancels.
- [ ] Approve with `y` (or `./bin/homerun run --pr N matthew-reed-holden/homerun-e2e`).
- [ ] `host` and `checks / shellcheck` are neutral "skipped: fork PR";
      `secret` prints an empty value.

## 4. First users

Each repo change is a feature branch and PR, merged per that repo's rules.
The user config holds one `app:` block, so pick the App first: install the
existing one on the org, or `homerun setup --org Lordhill-Digital` and switch
to it.

1. `Lordhill-Digital/theostack-backend`: `repo add`; `.github/homerun.yml`
   with `host_jobs: [docker]` plus any secrets `ci.yml` needs. On a real PR,
   `ci.yml`'s jobs report as `homerun / …` and `docker` runs on the host.
2. `Lordhill-Digital/theostack-frontend`: `repo add`; `.github/homerun.yml`
   with `max_parallel: 1` (tests need a single worker). `ci.yml` passes on a
   real PR.
3. `Lordhill-Digital/theostack-infra`: `repo add`; `.github/homerun.yml` with
   `checks: {terraform-validate: "terraform init -backend=false && terraform validate"}`.
   Push to `master`; `homerun / checks / terraform-validate` reports.
4. `homerun daemon --install` to run under systemd.

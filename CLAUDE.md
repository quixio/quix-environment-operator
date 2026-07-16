# Agent guide — quix-environment-operator

Kubernetes controller (Go, controller-runtime) that provisions isolated `Environment`
namespaces with scoped RBAC. Domain logic lives in `internal/` (resources/namespace,
resources/rolebinding, resources/environment, controller/environment, security, config).

## Verifying changes — read this before running tests

Run the canonical helper from any checkout or worktree:

```bash
.agents/verify.sh [worktree-dir]   # build + vet + integration suite
```

It encodes the non-obvious local setup. If you run `go test` by hand, replicate all of it:

- **Tests are integration-only.** Every `*_test.go` is tagged `//go:build integration` and
  lives in `./internal/`. `make test-unit` therefore runs **zero** tests and is not a real
  check. Real verification is the integration suite:
  `go test -tags integration ./internal/`.
- **envtest assets are in `bin-linux/`, not `bin/`.** The Makefile's `setup-envtest` resolves
  a darwin binary that cannot run on Linux. Point the env var at the linux assets:
  `KUBEBUILDER_ASSETS=<repo>/bin-linux/k8s/1.28.0-linux-arm64`.
- **Two generated files are git-ignored but required to compile** the suite:
  `api/v1/zz_generated.deepcopy.go` and `config/crd/bases/quix.io_environments.yaml`.
  `controller-gen` here is darwin-only, so don't `make generate`; seed these from the main
  checkout (the helper does this automatically).
- **`helm` is not installed.** Plans whose `test_command` is `make helm-lint` cannot run it;
  validate chart YAML under `deploy/quix-environment-operator/` by parsing it (e.g. with a
  YAML linter) and note that `helm lint` was unavailable.

## Work-queue delivery (for /work, /work-peered, /work-parallel)

- The async work queue lives in `.agents/work-queue/` (managed by `wq.py`).
- **Delivery branch is `task/review`**, not `main`. Every completed plan must be
  **squash-merged back into `task/review`** following the `commit-style` skill (type prefix,
  section bullets, no scope in the prefix, no hard wrapping). Do not mention review process,
  agents, or workflow steps in commit messages — describe the code change only.
- Create worktrees under `/tmp/qeo-wt/` — the repo's parent directory is read-only, so
  `../.worktrees` (the skills' default) fails. Branch each worktree off the current
  `task/review` tip so changes accumulate and merges stay conflict-free.

## Conventions

- Errors: wrapped `fmt.Errorf` with `%w`/`%q`. Events: `recorder.Eventf(env, EventType..., "Reason", ...)`.
- Use the label constants in `internal/resources/namespace` (`ManagedByLabel`,
  `LabelEnvironmentID`, `LabelEnvironmentName`) — never hardcode label keys.
- Tests use Ginkgo/Gomega over envtest in package `internal`.

# Contributing

Use Go 1.24 or later. The library uses only the standard library; development
checks use the tool versions pinned in `.github/workflows/ci.yml`.

Install golangci-lint v2.15.0 using its official release, then clone the project:

```sh
git clone https://github.com/dio/turngate.git
cd turngate
make fmt
make test
make vet
make lint
make vuln
make fuzz
```

`make fmt` applies `gci` import ordering (standard library, third-party, then
this module) and `gofumpt` formatting through golangci-lint v2.15.0.
`make lint` checks formatting and code quality without rewriting files; CI runs
the same configuration. Use `make lint-fix` for available automatic fixes.

`make fuzz` runs all three fuzz targets for 30 seconds each. For a longer local
campaign, use `make fuzz FUZZ_TIME=10m`. `FUZZ_PARALLEL` defaults to two workers.
A failing input is written under `testdata/fuzz/<target>/`; keep a minimized
reproduction as a regression test when fixing the failure. Ordinary `go test`
runs the seed corpus, but only `-fuzz` starts coverage-guided mutation.

The fuzz targets cover:

- `FuzzEvaluate`: tool names, shell commands, outputs, error flags and turn depth.
- `FuzzHistory`: multiple calls/results, namespaces, argv, bounded configuration,
  invalid state, input immutability and deterministic judgments.
- `FuzzScoreSignals`: floating-point edge cases, large counters, finite scores
  and mutually exclusive stall dimensions.

CI tests Go 1.24, 1.25, 1.26 and the current stable Go release on Linux, plus the
stable release on macOS. It runs race detection, randomized test order, formatting,
module hygiene, vet, pure-Go tests, lint and vulnerability scanning. Each fuzz
job runs for 30 seconds on pull requests, main/tag pushes and manual dispatches;
the weekly scheduled run uses ten minutes per target. Logs and fuzz corpora are
retained for 14 days. Inspect the artifacts when a job fails and commit the minimal
reproduction, not an entire generated corpus.

Preserve upstream attribution when changing adapted code. Keep compatibility
boundaries in DESIGN.md current. New heuristic rules need behavioral tests and
workload evidence before making quality or savings claims.

## Documentation and pull requests

Keep exported declarations and struct fields documented, including defaults,
limits, errors, state ownership and concurrency. Preview with `go doc -all .`.
Add runnable `Example` functions when introducing a new integration pattern;
`go test` verifies their output. Update README/USE_CASES/DESIGN when the public
contract changes. GitHub Releases serve as the changelog.

Submit a pull request with the behavior changed and checks run. The protected
main branch requires current CI and one approving review, including for admins.
Outside contributors need approval before their workflows run. Avoid private
repository paths, internal deployment details or credentials in code and docs.

## Releases

This is a library; no compiled binary package is needed. Publish a semantic-version
Git tag only after CI passes on the exact commit, then create a GitHub Release.
Use `v0.x.y` while the API is evolving. Never move or rewrite a published tag;
corrections require a new version. After publishing, verify availability with:

```sh
GOPROXY=https://proxy.golang.org go list -m github.com/dio/turngate@VERSION
```

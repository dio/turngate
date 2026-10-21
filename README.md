# turngate

[![CI](https://github.com/dio/turngate/actions/workflows/ci.yml/badge.svg)](https://github.com/dio/turngate/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/dio/turngate.svg)](https://pkg.go.dev/github.com/dio/turngate)

Turngate evaluates agent tool history locally and returns one of two configured
choice labels. It is a Go adaptation of NVIDIA NeMo Switchyard's heuristic Stage
Router. It has no runtime dependencies, network calls, model inference, history
store or provider credentials. Module: `github.com/dio/turngate` (Go 1.24+).

```text
request JSON -> caller's normalization -> turngate judgment
                                              |
                              approved label -> routing policy
                                              |
                                      model / provider
```

The caller decides which model/provider each label means. Turngate does not assess
provider health or select URLs. It is suitable for tool-using agents, not semantic
classification of a standalone prompt. Without tool activity it returns the
configured default, unless compaction forces escalation.

## Install

```sh
go get github.com/dio/turngate@v0.1.0
```

## Usage

```go
package main

import (
    "fmt"
    "log"

    "github.com/dio/turngate"
)

func main() {
    config := turngate.DefaultConfig()
    config.EfficientLabel = "routine"
    config.CapableLabel = "recovery"
    evaluator, err := turngate.New(config)
    if err != nil {
        log.Fatal(err)
    }

    history := turngate.History{Messages: []turngate.Message{
        {Calls: []turngate.ToolCall{{
            ID: "build-1", Name: "bash", Command: "make",
        }}},
        {Results: []turngate.ToolResult{{
            CallID: "build-1", Output: "out of memory", IsError: true,
        }}},
    }}
    result, nextState, err := evaluator.Evaluate(history, turngate.State{})
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result.Label, result.OverrideReason, nextState.CapableHoldRemaining)
    // recovery critical_error 2
    // Persist nextState once for this session and pass it on the next turn.
    // Map result.Label to an approved model/provider in the host application.
}
```

See [the executable examples](example_test.go) for provider/model mapping,
stateless evaluation, multi-turn holds and isolated scoring. The
[API reference](https://pkg.go.dev/github.com/dio/turngate) documents every
exported type and field. `Result` includes `Label`, `Tier`,
`Source`, `OverrideReason`, optional `Score`, and `Signals`. These diagnostics do
not contain request text. Heuristic confidence is the absolute signed score, not
an outcome probability. No probability distribution is returned.

## Inputs and state

Normalize provider JSON before calling the package. Decode tool arguments into
`Command` or `Argv`; other arguments are unnecessary for the heuristic. Provide
ordered messages containing tool calls and matching results, including IDs,
outputs and explicit error flags. `BareName` carries provider namespace metadata.
Ordinary text is used only for compaction markers. Input is checked for UTF-8 and
bounded to 64 KiB of string evidence (including argv separators), 1,024 messages,
and 4,096 call/result events. The byte bound is not the serialized JSON size.
Excess input returns an error without changing state or fabricating a judgment.

There is **no moving average and no history database**. Each evaluation rebuilds
signals from the history supplied with that request. The recent window defaults
to three calls for operation counts and three nonempty/failed results for errors.
Severity is the maximum in that result window. Empty successful results count as
results but do not advance the text window; retrieved nonempty content contributes
a clean placeholder. This follows the pinned extractor, whose call and result
windows are separate.

`State` stores only remaining capable-hold turns. The caller owns storage and
must isolate it by principal, scope, session, agent and configuration generation.
Serialize updates within that key; compute and persist once per logical agent
turn. Reuse the result for request retries. A default state starts a new session.
Set `CapableHoldTurns` to zero for stateless evaluation. Passing zero state each
time also disables continuity. The package neither fetches `previous_response_id`
history nor reconstructs omitted conversation messages.

## Configuration and errors

Start with `DefaultConfig()` and override fields before calling `New`.

| Setting | Default | Contract |
| --- | --- | --- |
| Labels | `efficient`, `capable` | Distinct, nonblank UTF-8 strings, at most 128 bytes each |
| Default tier | `Efficient` | Used for absent or ambiguous evidence |
| Confidence threshold | `0.5` | Finite value in `[0,1]`; the ambiguous band includes its boundaries |
| Recent window | `3` | Independent call/result windows, from 1 to 4,096 |
| Capable hold | `2` | Additional held evaluations, from 0 to 4,096 |
| Tool semantics | None | Up to 4,096 custom mappings; built-in names cannot be replaced |

`New` rejects invalid configuration and copies the custom map. An evaluator is
safe to share across goroutines; callers must not mutate history slices while an
evaluation reads them. Its zero value is invalid.

`Evaluate` rejects invalid UTF-8, oversized evidence, blank tool names, conflicting
command representations, excessive argv length and a hold counter above the
configured duration. It returns a zero `Result` and the unchanged previous state
on error. Handle the error before using the label; fallback routing is the host's
policy. Error strings are diagnostic text, not stable machine-readable codes.
Neither input nor configuration is silently truncated.

## Policy

Start from `DefaultConfig()`; `Config{}` is intentionally invalid. Explicit zero
threshold and zero hold duration retain their meanings.

- Clean production favors the efficient tier.
- Errors and deep investigation/stalling favor the capable tier.
- Critical failures, repeated material failures and compaction force escalation.
- An active hold retains capable for two further evaluations by default. A clean
  recent test pass clears it early. A new failure during an active hold consumes
  that hold rather than extending it; persistent failure can escalate again later.
- Ambiguous evidence uses `DefaultTier`. `ConfidenceThreshold` defines a closed
  ambiguous band, so exactly-on-threshold scores use the default.

Custom `ToolSemantics` add exact names for `Observe`, `Mutate`, `Plan` and
`NewActivity`. They are copied at construction. Built-in names cannot be replaced.
Custom observation tools still have their output scanned for diagnostics; they do
not acquire the built-in retrieval exemption. A new-activity tool suppresses stall
signals without favoring a tier. No user-defined code or tool is executed.

## Verification and limits

```sh
GOWORK=off go test -race -shuffle=on ./...
GOWORK=off go vet ./...
CGO_ENABLED=0 GOWORK=off go test ./...
make fuzz FUZZ_TIME=30s
```

CI runs race tests, lint, vulnerability checks and active fuzzing on pull requests
and pushes, with longer weekly fuzz campaigns. See [contributing](CONTRIBUTING.md)
for targets and reproduction instructions.

The tests include source-derived numerical fixtures, scenarios, concurrent calls,
and input fuzzing. They do not establish live task success, savings, or complete
Rust runtime equivalence. See [design and provenance](DESIGN.md),
[fixture provenance](testdata/README.md), [research evidence](RESEARCH.md), and
[possible use cases](USE_CASES.md).

Apache-2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE).

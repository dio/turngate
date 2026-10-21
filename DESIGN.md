# Design and compatibility

Turngate is an immutable evaluator over normalized conversation history. The
consumer owns identity, deadlines, input decoding, routing maps, state storage,
provider selection, authorization, metering and transport. No gateway-specific types or
Rust dependencies enter this module.

## Public boundary

`New(Config)` validates labels, finite thresholds, window/hold bounds and additive
tool semantics. `Evaluate(History, State)` returns `(Result, State, error)` and
leaves its arguments unchanged. Failed evaluation returns the previous state and
an empty judgment. `ScoreSignals` permits isolated inspection of extracted signals
and rejects invalid severity; it does not apply overrides or state.

A choice has a label and diagnostic origin. Score is absent when an override, a
hold or absent tool history determines the result. An override has its specific
reason. Scores are not supplied as probabilities to downstream decision consumers.

`Message` groups calls before results. Normal OpenAI/Anthropic tool exchanges fit
that shape. An adapter with interleaved call/result blocks must preserve causal
ordering; splitting into messages changes the depth proxy and must be accounted
for or rejected by that adapter. No universal wire decoder is included.

## Pinned behavior

Source: NVIDIA-NeMo/Switchyard at
`7dddc65a099704a97cb1dd0f4858acc15ac2cda5`, specifically:

- `crates/libsy/src/algorithms/util/stage.rs`
- `crates/libsy/src/algorithms/util/tool_signals.rs`

The score uses float32 severity promoted to float64, matching the reference.
At depth eight or above, no production and no new activity indicates spinning
or exploring, depending on observation/planning. These dimensions are mutually
exclusive. Production is the fraction of recognized recent read/write/edit/plan
operations that are writes or edits. New activity is excluded from its denominator.

```text
raw = 0.10 × (severity / 0.7 + spinning + exploring
              - 1.5 × production_intensity - clean_production)
score = tanh(5 × raw)
clean_production = 1.2 when production_intensity > 0 and severity == 0, else 0
```

The tier comparison preserves the reference's transformed score-space comparison
and closed thresholds. The intermediate `(score + 1) / 2` is never published as a
probability. A capable decision from dimensions or an override starts the hold.
A capable default does not start one. An active hold is considered before a new
override, as in the source classifier. Clearing a hold does not erase an error
still inside the result window: the ordinary rules can escalate again.

Failed output recognition preserves the reference's severity patterns, numeric
exit-status handling, compiler/runtime/patch diagnostics, repeated-failure
normalization, and test-pass handling. Successfully retrieved content does not
become execution evidence. Missing-file observations are exempt, including
explicitly flagged missing-file reads. Custom observations do not have this
exemption. Reused call IDs bind results to the most recent preceding call.

Shell handling separates activity recognition from the stricter retrieval
exemption. A mixed read/execute command can count as observation while retaining
its output diagnostics. Standard wrappers and common inspection commands are
recognized, with a 20-level recursion bound and 16 KiB shell-recognition bound.
This is a heuristic parser, not a safety or command-execution policy.

## Deliberate boundaries and differences

- Wire-specific JSON, unknown provider blocks and argument-string decoding belong
  to the adapter. Map built-in shell/patch outputs to normalized calls/results,
  preserving failure metadata; reject unsupported shapes rather than dropping them.
- Opaque JavaScript/code wrappers stay unknown. Unlike the reference's small
  straight-line `exec.input` patch recognizer, Turngate does not inspect executable
  wrappers. An adapter may report actual observed nested operations separately.
- Shell tokenization is a small Go implementation, not the Rust `shlex` crate.
  Tests cover common wrappers, quoting, substitutions and mixed execution. Full
  shell grammar parity is not claimed; ambiguous syntax retains diagnostics.
- No tool activity means default/abstention even at deep chat-only history, and
  preserves a supplied hold without consuming it. Compaction can still override.
  This makes the advertised tool-history requirement explicit instead of deriving
  a stall solely from the message count.
- Config/input limits and error handling are this package's contract, not a port
  of the Switchyard deployment schema. Holds are bounded to 4,096 turns.
- Custom tool matching is ASCII case-insensitive; built-in names use lowercase
  comparison. Bare-name selection is caller metadata or the conventional MCP form.
- There are two relative capability tiers. Supporting an arbitrary number of
  semantic categories requires a different policy; renaming labels does not teach
  this heuristic the meaning of natural-language criteria.
- No classifier fallback, prompts, networking, metrics backend, hidden cache,
  protocol translation or session map is included.

## Validation claim

Tests constrain this package's contract and reproduce expected behavior read from
the pinned source and upstream tests. The numerical fixtures are analytically
computed reference values, not captured Rust output. Cross-language executable parity remains unverified. A future parity runner should feed equivalent normalized histories to
`ToolSignals::from_request_with_semantics` and compare signals, decisions and
multi-turn holds, reporting intentional differences above separately.

This is an engineering heuristic, not a calibrated predictor. End-to-end evaluation
must fix the agent harness and model pair, compare always-efficient/always-capable
baselines, and measure task success, cost, latency and escalation rates. Do not
infer quality from a routing split or unit tests.

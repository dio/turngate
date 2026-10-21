# Possible use cases

Turngate produces a local, two-tier judgment from tool activity. The caller maps
its labels to configured models/providers and owns the actual model calls. These
are integration patterns, not bundled gateways or measured quality guarantees.

## Coding-agent model selection

A coding agent can use an efficient model for clean editing and a more capable
model when tool results show recovery work. Send the conversation's tool calls
and outputs to `Evaluate` before the next model request. Map the returned label
to an approved model/provider in the host application.

```text
agent tool history -> Turngate -> routine  -> efficient model/provider
                              -> recovery -> capable model/provider
```

Configure the model pair for the task and verify end-to-end success against
always-efficient and always-capable baselines. A routing split alone does not
establish savings or maintained quality.

## Stateless gateway evaluation

A gateway can evaluate each full-history request locally, avoiding a separate
judge-model call. Set `CapableHoldTurns = 0` and pass an empty `State`. The gateway
can then use the choice to select its existing routing policy, including its own
provider health checks, retries and fallback.

Turngate consumes normalized Go structs, not a provider's request JSON. The
adapter must preserve tool names, decoded command arguments, result linkage,
explicit failures and message boundaries. A request containing only a continuation
ID needs history reconstruction before evaluation.

## Recovery across agent turns

An agent runtime with session state can retain a short capable hold after
escalation. Store the returned `State` once per logical turn and supply it with
the next evaluation. A clean recent test pass can clear the hold early.

Keep state isolated per authenticated session, agent and evaluator configuration.
Serialize updates and reuse a decision on retries. The state is only a counter;
Turngate does not need a history database when requests already carry history.

## Domain-specific tools

An application can add exact tool-name mappings:

```go
config := turngate.DefaultConfig()
config.ToolSemantics = map[string]turngate.Semantic{
    "search_inventory": turngate.Observe,
    "update_inventory": turngate.Mutate,
    "create_plan":      turngate.Plan,
    "send_update":      turngate.NewActivity,
}
```

These mappings feed the existing heuristic. They do not teach it domain-specific
reasoning or verify that a tool succeeded. Custom observations retain output
error scanning. For high-impact operations, keep authorization and approval
policy separate from model selection.

## Offline trace analysis

Evaluate recorded normalized histories to inspect escalation reasons, window
sizes and thresholds. Diagnostic signals contain no original prompt/output text.
Replay is useful for explaining decisions, but cannot prove how a different
model would have changed subsequent agent actions or final task success.

## Where another policy is needed

Turngate does not classify an isolated prompt semantically, rank an arbitrary
model catalog, choose the healthiest provider, or enforce tool permissions.
Without tool activity it returns the default label, except for compaction.
Use a separate policy for these decisions; score confidence is not an outcome
probability.

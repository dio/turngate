# Research evidence

Reviewed 2026-10-21. This implementation follows the pinned Switchyard code, not a
paper-derived learned routing model. No paper validating the exact Stage Router
weights, window size or hold duration was found in the reviewed NVIDIA material
or targeted literature searches. That is a bounded search finding, not a claim
that no such paper exists.

## Closest evidence

[NVIDIA's Switchyard article](https://developer.nvidia.com/blog/route-ai-agent-workloads-across-models-with-nvidia-nemo-switchyard)
describes tuning-free routers including Stage Router. Its
[Stage Router documentation](https://nvidia-nemo.github.io/Switchyard/routing_algorithms/stage_router_routing/)
explains the score and recommends calibration on representative agent histories.
This is engineering documentation and benchmark guidance. It does not establish
that every heuristic transfers to a new model pair or workload.

[RouteLLM: Learning to Route LLMs with Preference Data](https://arxiv.org/abs/2406.18665),
[ICLR 2025 paper](https://openreview.net/pdf?id=8sSqNntaMr), supports learned selection
between stronger and cheaper models to trade cost against response quality. Its
preference-trained routers do not validate Turngate's fixed tool-signal rules.

[Agentic Routing: The Harness-Native Data Flywheel](https://arxiv.org/abs/2607.11399)
(2026 preprint) considers step-level routing conditioned on harness state and
execution feedback. It is closer to Turngate's observation boundary, but its
learned ranking/policy machinery differs from this heuristic.

## Evaluation needed before quality claims

Use fixed task sets and fixed model/provider versions. Compare always-efficient,
always-capable and Turngate decisions under the same harness and budgets. Measure
completed-task correctness, billed cost, end-to-end latency, escalations, holds
and failures. Use repeated trials and uncertainty estimates for stochastic agents.
Ablate the result window, hold and hard overrides. Calibrate on one partition and
report held-out tasks separately. A replay can test decisions, but cannot establish
counterfactual task success after a different model changes subsequent history.

No live task-quality or cost benchmark is part of the core implementation checks.

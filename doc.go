// SPDX-License-Identifier: Apache-2.0

// Package turngate derives a local, two-tier routing judgment from agent tool
// history. It makes no model calls, performs no I/O and has no runtime dependencies.
// The host maps the returned label to an approved model/provider and performs
// the actual request.
//
// # Getting started
//
// Create an immutable [Evaluator] with [New] and [DefaultConfig]. Normalize the
// provider's request into [History], preserving chronological message boundaries,
// tool names, decoded commands, result linkage and explicit failures. Call
// [Evaluator.Evaluate] before the next model request. The package does not decode
// provider JSON or reconstruct history from continuation IDs.
//
// # State and concurrency
//
// An evaluator can serve concurrent sessions. Each session supplies its own
// history and [State]; there is no internal history store or moving average.
// Persist returned state once per logical agent turn and reuse decisions on
// retries. Isolate state by principal, scope, session, agent and configuration
// generation, and serialize updates for that key. Do not mutate history while
// an evaluation reads it. Set [Config.CapableHoldTurns] to zero and pass State{}
// for stateless routing.
//
// # Decisions and limitations
//
// Critical/repeated failures and compaction can force escalation. Otherwise,
// signed heuristic evidence selects a tier outside the configured ambiguous band.
// Existing holds retain the capable tier before ordinary escalation rules; a
// clean test pass can clear them. Missing tool activity uses the default without
// consuming an existing hold, unless compaction overrides.
//
// [Result] exposes the chosen label, decision source and diagnostic signals.
// [Score.Confidence] is heuristic magnitude, not a calibrated probability.
// [ScoreSignals] is available for isolated diagnostics and does not apply holds
// or overrides. Invalid input returns an error and preserves prior state; the
// host owns fallback policy. The zero [Config] and zero [Evaluator] are invalid.
//
// This heuristic is adapted from NVIDIA NeMo Switchyard. It is intended for
// tool-using agents, not standalone prompt classification, provider-health
// selection or authorization. Workload quality and savings require separate
// end-to-end measurement. See the repository's DESIGN.md and NOTICE for
// provenance and compatibility boundaries.
package turngate

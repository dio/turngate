// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
// SPDX-License-Identifier: Apache-2.0
// Adapted from Switchyard stage.rs; see NOTICE for revision and modifications.

package turngate

import (
	"fmt"
	"math"
)

// Signals are observations extracted from the available conversation. Recent
// counters cover calls; severity and repeated failure cover nonempty/failed results.
// Severity retains Rust's float32 representation for reference compatibility.
type Signals struct {
	// Severity is the maximum diagnostic severity in the recent result
	// window, in [0,1]; 1 represents a critical failure.
	Severity float32
	// RepeatedFailure reports a repeated normalized material failure in the
	// recent result window.
	RepeatedFailure bool
	// NoErrorStreak counts consecutive clean eligible results at the end of
	// all supplied history, not just the recent window. Retrieval placeholders
	// are clean; empty successful outputs do not advance this counter.
	NoErrorStreak uint32
	// RecentWrites counts recognized writes in the recent call window.
	RecentWrites uint32
	// RecentEdits counts recognized edits in the recent call window.
	RecentEdits uint32
	// RecentReads counts recognized observations in the recent call window.
	RecentReads uint32
	// RecentPlans counts planning calls in the recent call window.
	RecentPlans uint32
	// RecentNew counts custom NewActivity calls in the recent call window.
	RecentNew uint32
	// ToolCallCount counts all supplied calls, including unknown tools.
	ToolCallCount uint32
	// ToolResultCount counts all supplied results, including empty successes.
	ToolResultCount uint32
	// TurnDepth is the number of supplied messages, not logical agent turns.
	TurnDepth uint32
	// TestsPassed reports a recognized test pass with no later failed or
	// positive-severity result in the recent window. It is a text heuristic.
	TestsPassed bool
	// Compacted is true for adapter metadata or recognized text markers.
	Compacted bool
}

// Score describes signed heuristic evidence. Positive favors capable, negative
// favors efficient. Confidence is abs(Value), not a probability of correctness.
type Score struct {
	// Value is the signed score in [-1,1]: positive favors Capable.
	Value float64
	// Confidence is abs(Value), in [0,1], not a probability.
	Confidence float64
	// Severity is the extracted float32 severity promoted to float64.
	Severity float64
	// Spinning is 1 at depth >= 8 with no recent production, new activity,
	// reads or plans; otherwise 0. It is mutually exclusive with Exploring.
	Spinning float64
	// Exploring is 1 at depth >= 8 with recent reads/plans but no production
	// or new activity; otherwise 0.
	Exploring float64
	// ProductionIntensity is (writes+edits)/(writes+edits+reads+plans),
	// or zero if the denominator is zero. New activity is excluded.
	ProductionIntensity float64
}

// ScoreSignals computes the diagnostic score without choosing a label or applying
// overrides/holds. Signals should come from Result.Signals; manually constructed
// signals require a finite severity in [0,1]. Invalid severity returns a zero
// Score and an error. Counters are accepted as given, without consistency checks;
// use extracted signals when interpreting results as actual history evidence.
func ScoreSignals(s Signals) (Score, error) {
	if math.IsNaN(float64(s.Severity)) || math.IsInf(float64(s.Severity), 0) || s.Severity < 0 || s.Severity > 1 {
		return Score{}, fmt.Errorf("turngate: severity must be finite and in [0,1]")
	}
	return scoreSignals(s), nil
}

func scoreSignals(s Signals) Score {
	d := Score{Severity: float64(s.Severity)}
	production := float64(s.RecentWrites) + float64(s.RecentEdits)
	operations := production + float64(s.RecentReads) + float64(s.RecentPlans)
	if operations > 0 {
		d.ProductionIntensity = production / operations
	}
	if s.TurnDepth >= 8 && production == 0 && s.RecentNew == 0 {
		if s.RecentReads > 0 || s.RecentPlans > 0 {
			d.Exploring = 1
		} else {
			d.Spinning = 1
		}
	}
	clean := 0.0
	if d.ProductionIntensity > 0 && d.Severity == 0 {
		clean = 1.2
	}
	raw := 0.10 * (d.Severity/0.7 + d.Spinning + d.Exploring - 1.5*d.ProductionIntensity - clean)
	d.Value = math.Tanh(5.0 * raw)
	d.Confidence = math.Abs(d.Value)
	return d
}

func overrideReason(s Signals) string {
	if s.Compacted {
		return "compaction"
	}
	if s.Severity >= 1 {
		return "critical_error"
	}
	if s.RepeatedFailure {
		return "repeated_failure"
	}
	return ""
}

func (e *Evaluator) evaluateSignals(s Signals, previous State) Result {
	result := Result{Tier: e.config.DefaultTier, Source: Default, Signals: s}
	clean := s.TestsPassed && s.NoErrorStreak > 0
	// With no activity the signal scorer abstains. Compaction remains an override.
	if s.ToolCallCount == 0 && s.ToolResultCount == 0 && !s.Compacted {
		return e.label(result)
	}
	if !clean && previous.CapableHoldRemaining > 0 {
		result.Tier = Capable
		result.Source = CapableHold
	} else if reason := overrideReason(s); reason != "" {
		result.Tier = Capable
		result.Source = Override
		result.OverrideReason = reason
	} else {
		score := scoreSignals(s)
		result.Score = &score
		// Preserve upstream's probability-space comparisons, including closed edges.
		// This intermediate transform is not an exported probability.
		position := (score.Value + 1) / 2
		half := e.config.ConfidenceThreshold / 2
		if position > 0.5+half || position < 0.5-half {
			result.Source = Dimensions
			result.Tier = Efficient
			if position > 0.5 {
				result.Tier = Capable
			}
		}
	}
	return e.label(result)
}

func (e *Evaluator) label(r Result) Result {
	r.Label = e.config.EfficientLabel
	if r.Tier == Capable {
		r.Label = e.config.CapableLabel
	}
	return r
}

func (e *Evaluator) nextState(r Result, previous State) State {
	s := r.Signals
	if s.TestsPassed && s.NoErrorStreak > 0 {
		previous.CapableHoldRemaining = 0
	}
	if r.Source == CapableHold {
		previous.CapableHoldRemaining--
	}
	if r.Tier == Capable && (r.Source == Override || r.Source == Dimensions) {
		previous.CapableHoldRemaining = e.config.CapableHoldTurns
	}
	return previous
}

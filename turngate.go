// SPDX-License-Identifier: Apache-2.0

// Package turngate evaluates agent tool history locally and returns a configured
// choice label. It performs no I/O, model calls, or destination selection.
package turngate

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// Evidence bounds apply before any text parsing. Inputs exceeding them are errors,
// never silently truncated into a different decision.
const (
	MaxEvidenceBytes = 64 * 1024
	MaxMessages      = 1024
	MaxToolEvents    = 4096
)

// Tier is a relative capability requirement, not a model or destination.
type Tier string

const (
	Efficient Tier = "efficient"
	Capable   Tier = "capable"
)

// Source explains which decision rule selected the label.
type Source string

const (
	Default     Source = "default"
	Dimensions  Source = "dimensions"
	Override    Source = "override"
	CapableHold Source = "capable_hold"
)

// Semantic adds a meaning for an otherwise unknown tool name. Observe does not
// suppress output diagnostics: only recognized built-in retrieval tools do that.
type Semantic string

const (
	Observe     Semantic = "observe"
	Mutate      Semantic = "mutate"
	Plan        Semantic = "plan"
	NewActivity Semantic = "new"
)

// Config maps the two capability tiers to consumer-owned labels. Use
// DefaultConfig and then change fields; zero threshold and zero hold are meaningful.
type Config struct {
	EfficientLabel      string
	CapableLabel        string
	DefaultTier         Tier
	ConfidenceThreshold float64
	RecentWindow        int
	CapableHoldTurns    uint32
	ToolSemantics       map[string]Semantic
}

// DefaultConfig returns the signal-only efficient-first policy.
func DefaultConfig() Config {
	return Config{
		EfficientLabel: "efficient", CapableLabel: "capable", DefaultTier: Efficient,
		ConfidenceThreshold: 0.5, RecentWindow: 3, CapableHoldTurns: 2,
	}
}

// Evaluator is immutable after construction and safe for concurrent evaluations.
// Its zero value is invalid; construct it with New.
type Evaluator struct {
	config      Config
	initialized bool
}

// New validates and copies configuration. No caller-owned map is retained.
func New(config Config) (*Evaluator, error) {
	for _, label := range []string{config.EfficientLabel, config.CapableLabel} {
		if len(label) > 128 || !utf8.ValidString(label) || strings.TrimSpace(label) == "" {
			return nil, fmt.Errorf("turngate: labels must be nonblank UTF-8, at most 128 bytes")
		}
	}
	if config.EfficientLabel == config.CapableLabel {
		return nil, fmt.Errorf("turngate: labels must differ")
	}
	if config.DefaultTier != Efficient && config.DefaultTier != Capable {
		return nil, fmt.Errorf("turngate: invalid default tier")
	}
	if math.IsNaN(config.ConfidenceThreshold) || math.IsInf(config.ConfidenceThreshold, 0) || config.ConfidenceThreshold < 0 || config.ConfidenceThreshold > 1 {
		return nil, fmt.Errorf("turngate: threshold must be finite and in [0,1]")
	}
	if config.RecentWindow < 1 || config.RecentWindow > MaxToolEvents {
		return nil, fmt.Errorf("turngate: recent window must be in [1,%d]", MaxToolEvents)
	}
	if config.CapableHoldTurns > MaxToolEvents {
		return nil, fmt.Errorf("turngate: hold must be at most %d", MaxToolEvents)
	}
	if len(config.ToolSemantics) > MaxToolEvents {
		return nil, fmt.Errorf("turngate: too many tool semantics")
	}
	copied := make(map[string]Semantic, len(config.ToolSemantics))
	for name, meaning := range config.ToolSemantics {
		if len(name) > 128 || !utf8.ValidString(name) || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("turngate: invalid tool name")
		}
		key := asciiLower(name)
		if builtin(key) {
			return nil, fmt.Errorf("turngate: built-in tool %q cannot be reclassified", name)
		}
		if _, ok := copied[key]; ok {
			return nil, fmt.Errorf("turngate: duplicate tool name %q", name)
		}
		switch meaning {
		case Observe, Mutate, Plan, NewActivity:
		default:
			return nil, fmt.Errorf("turngate: invalid meaning for tool %q", name)
		}
		copied[key] = meaning
	}
	config.ToolSemantics = copied
	return &Evaluator{config: config, initialized: true}, nil
}

// History is the complete normalized conversation available for this turn.
// Retain message boundaries: depth is the message count, not tool count.
// Compacted is trusted adapter metadata, not a user-selected routing instruction.
type History struct {
	Messages  []Message
	Compacted bool
}

// Message preserves ordered calls and results within a conversation message.
// Calls precede results in this normalized representation. Split a message with
// interleaved results/calls before adapting, and account for that depth difference.
// Text is conversation text, not tool output. It is scanned for compaction only.
type Message struct {
	Text    string
	Calls   []ToolCall
	Results []ToolResult
}

// ToolCall contains already decoded arguments relevant to signal extraction.
// Set Command OR Argv, never both. BareName is supplied for provider namespace
// forms; MCP mcp__server__tool names are also recognized without it.
// Code is an opaque executable wrapper: it remains unknown, rather than being
// mistaken for shell. Adapters may unwrap only operations they actually observed.
type ToolCall struct {
	ID       string
	Name     string
	BareName string
	Command  string
	Argv     []string
	Code     string
}

// ToolResult links output to its preceding call. Unknown IDs are allowed and
// conservatively retain diagnostics. Reused IDs refer to the latest prior call.
// Empty successful outputs count as results but do not advance the error window,
// matching the pinned Switchyard text-extraction behavior.
type ToolResult struct {
	CallID  string
	Output  string
	IsError bool
}

// State belongs to exactly one principal/scope/session/agent/config generation.
// The caller stores it and serializes updates. It contains no conversation text.
type State struct{ CapableHoldRemaining uint32 }

// Result is a choice plus bounded diagnostic data. There are deliberately no
// probabilities: heuristic confidence is not calibrated outcome probability.
// Score is nil for overrides, holds and missing tool history.
type Result struct {
	Label          string
	Tier           Tier
	Source         Source
	OverrideReason string
	Score          *Score
	Signals        Signals
}

// Evaluate extracts signals and evaluates one turn without mutating its inputs.
// On error it returns no judgment and the original state. Persist the next state
// once per agent turn; retries should reuse the original judgment.
func (e *Evaluator) Evaluate(history History, previous State) (Result, State, error) {
	if e == nil || !e.initialized {
		return Result{}, previous, fmt.Errorf("turngate: evaluator must be constructed with New")
	}
	if previous.CapableHoldRemaining > e.config.CapableHoldTurns {
		return Result{}, previous, fmt.Errorf("turngate: state hold exceeds configured duration")
	}
	if err := validateHistory(history); err != nil {
		return Result{}, previous, err
	}
	signals := e.extract(history)
	result := e.evaluateSignals(signals, previous)
	return result, e.nextState(result, previous), nil
}

func validateHistory(h History) error {
	if len(h.Messages) > MaxMessages {
		return fmt.Errorf("turngate: too many messages")
	}
	size, events := 0, 0
	add := func(s string) bool {
		if len(s) > MaxEvidenceBytes-size || !utf8.ValidString(s) {
			return false
		}
		size += len(s)
		return true
	}
	for _, m := range h.Messages {
		if !add(m.Text) {
			return fmt.Errorf("turngate: invalid or oversized evidence")
		}
		events += len(m.Calls) + len(m.Results)
		if events > MaxToolEvents {
			return fmt.Errorf("turngate: too many tool events")
		}
		for _, c := range m.Calls {
			if len(c.Argv) > MaxToolEvents {
				return fmt.Errorf("turngate: too many command arguments")
			}
			for _, s := range []string{c.ID, c.Name, c.BareName, c.Command, c.Code} {
				if !add(s) {
					return fmt.Errorf("turngate: invalid or oversized evidence")
				}
			}
			if strings.TrimSpace(c.Name) == "" || (c.Command != "" && len(c.Argv) > 0) || (c.Code != "" && (c.Command != "" || len(c.Argv) > 0)) {
				return fmt.Errorf("turngate: invalid normalized tool call")
			}
			for _, s := range c.Argv {
				if !add(s) || !add(" ") {
					return fmt.Errorf("turngate: invalid or oversized evidence")
				}
			}
		}
		for _, r := range m.Results {
			if !add(r.CallID) || !add(r.Output) {
				return fmt.Errorf("turngate: invalid or oversized evidence")
			}
		}
	}
	return nil
}

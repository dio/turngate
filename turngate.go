// SPDX-License-Identifier: Apache-2.0

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
	// MaxEvidenceBytes bounds the sum of all history string bytes, plus one byte
	// per argv element. It does not measure serialized JSON size.
	MaxEvidenceBytes = 64 * 1024
	// MaxMessages bounds the number of messages in one evaluation.
	MaxMessages = 1024
	// MaxToolEvents bounds the combined call/result count and each argv length.
	// It also bounds configuration window size, hold duration and mapping count.
	MaxToolEvents = 4096
)

// Tier is a relative capability requirement, not a model or destination.
type Tier string

const (
	// Efficient requests the lower-cost capability tier configured by the caller.
	Efficient Tier = "efficient"
	// Capable requests the higher-capability tier configured by the caller.
	Capable Tier = "capable"
)

// Source explains which decision rule selected the label.
type Source string

const (
	// Default means missing tool activity or a score inside the ambiguous band.
	Default Source = "default"
	// Dimensions means the signed score exceeded the configured threshold.
	Dimensions Source = "dimensions"
	// Override means compaction, critical failure or repeated failure escalated.
	Override Source = "override"
	// CapableHold means an existing hold retained the capable tier.
	CapableHold Source = "capable_hold"
)

// Semantic adds a meaning for an otherwise unknown tool name. Observe does not
// suppress output diagnostics: only recognized built-in retrieval tools do that.
type Semantic string

const (
	// Observe counts a custom tool as a read, without exempting its diagnostics.
	Observe Semantic = "observe"
	// Mutate counts a custom tool as a write.
	Mutate Semantic = "mutate"
	// Plan counts a custom tool as planning activity.
	Plan Semantic = "plan"
	// NewActivity suppresses stall signals without favoring either tier.
	NewActivity Semantic = "new"
)

// Config maps the two capability tiers to consumer-owned labels. Use
// DefaultConfig and then change fields; zero threshold and zero hold are meaningful.
type Config struct {
	// EfficientLabel is returned for Efficient. It must be nonblank UTF-8, at
	// most 128 bytes, and differ from CapableLabel; it is not trimmed.
	EfficientLabel string
	// CapableLabel is returned for Capable and has the same constraints as
	// EfficientLabel. Labels are opaque to the evaluator.
	CapableLabel string
	// DefaultTier is Efficient or Capable, used when evidence is absent or
	// ambiguous. A capable default does not start a hold.
	DefaultTier Tier
	// ConfidenceThreshold is finite and in [0,1]. Scores within the closed
	// band [-threshold,+threshold] use DefaultTier; overrides bypass it.
	ConfidenceThreshold float64
	// RecentWindow is in [1,MaxToolEvents]. It independently limits recent
	// calls and eligible results; these windows need not cover the same turns.
	RecentWindow int
	// CapableHoldTurns is in [0,MaxToolEvents]. Escalation starts this many
	// further held evaluations; zero disables holds.
	CapableHoldTurns uint32
	// ToolSemantics optionally maps at most MaxToolEvents custom tool names.
	// Names must be nonblank UTF-8 of at most 128 bytes. Matching folds ASCII
	// case; case-fold duplicates, built-in names and invalid semantics are
	// rejected. New copies the map; nil uses only built-in recognition.
	ToolSemantics map[string]Semantic
}

// DefaultConfig returns labels "efficient" and "capable", default tier Efficient,
// threshold 0.5, recent window 3, hold duration 2 and no custom tool mappings.
// Config{} is invalid; start here when overriding only selected settings.
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
// It returns nil and an error for any violated Config field constraint.
// Once New returns, the caller may change the original configuration and map.
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
	// Messages are in chronological order, including message boundaries.
	// A nil slice is valid and represents empty history.
	Messages []Message
	// Compacted reports context compaction observed by the adapter. Text
	// markers can also set the extracted signal.
	Compacted bool
}

// Message preserves ordered calls and results within a conversation message.
// Calls precede results in this normalized representation. Split a message with
// interleaved results/calls before adapting, and account for that depth difference.
// Text is conversation text, not tool output. It is scanned for compaction only.
type Message struct {
	// Text is conversation text scanned only for compaction markers.
	Text string
	// Calls are ordered tool invocations, processed before Results.
	Calls []ToolCall
	// Results are ordered outputs associated with preceding calls.
	Results []ToolResult
}

// ToolCall contains already decoded arguments relevant to signal extraction.
// Set Command OR Argv, never both. BareName is supplied for provider namespace
// forms; MCP mcp__server__tool names are also recognized without it.
// Code is an opaque executable wrapper: it remains unknown, rather than being
// mistaken for shell. Adapters may unwrap only operations they actually observed.
type ToolCall struct {
	// ID links subsequent results to this call. Empty IDs are allowed but
	// cannot establish retrieval exemptions; reused IDs replace prior linkage.
	ID string
	// Name is the nonblank tool name. Unknown names remain valid evidence.
	Name string
	// BareName optionally supplies a provider namespace-free name. If empty,
	// conventional mcp__server__tool names can supply it automatically.
	BareName string
	// Command is the decoded shell command or editor operation (such as view).
	// It is mutually exclusive with nonempty Argv and Code.
	Command string
	// Argv contains decoded command tokens, at most MaxToolEvents entries.
	// It is mutually exclusive with nonempty Command and Code.
	Argv []string
	// Code records an opaque executable wrapper. It is not parsed or executed
	// and prevents activity/retrieval classification for this call.
	Code string
}

// ToolResult links output to its preceding call. Unknown IDs are allowed and
// conservatively retain diagnostics. Reused IDs refer to the latest prior call.
// Empty successful outputs count as results but do not advance the error window,
// matching the pinned Switchyard text-extraction behavior.
type ToolResult struct {
	// CallID refers to the latest preceding nonempty ToolCall.ID. Unknown or
	// empty IDs are valid and retain output diagnostics.
	CallID string
	// Output is decoded tool output, not conversation text. All strings in
	// History must be valid UTF-8 and fit the aggregate evidence limit.
	Output string
	// IsError preserves explicit failure metadata even when Output is empty.
	// Built-in missing-file retrieval exemptions still apply.
	IsError bool
}

// State belongs to exactly one principal/scope/session/agent/config generation.
// The caller stores it and serializes updates. It contains no conversation text.
type State struct {
	// CapableHoldRemaining counts remaining held evaluations. The zero value
	// starts a session; values above Config.CapableHoldTurns are rejected.
	CapableHoldRemaining uint32
}

// Result is a choice plus bounded diagnostic data. There are deliberately no
// probabilities: heuristic confidence is not calibrated outcome probability.
// Score is nil for overrides, holds and missing tool history.
type Result struct {
	// Label is one of the two configured labels on successful evaluation.
	Label string
	// Tier is the relative capability requirement underlying Label.
	Tier Tier
	// Source identifies the decision rule; see the Source constants.
	Source Source
	// OverrideReason is compaction, critical_error or repeated_failure for
	// Override, and empty otherwise.
	OverrideReason string
	// Score holds heuristic diagnostics when scoring ran, including ambiguous
	// scores. It is nil for overrides, holds and absent tool activity.
	Score *Score
	// Signals contains extracted counters and flags, without original text.
	Signals Signals
}

// Evaluate extracts signals and evaluates one turn without mutating its inputs.
// On error it returns no judgment and the original state. Persist the next state
// once per agent turn; retries should reuse the original judgment.
//
// Errors include a nil/zero evaluator, hold state exceeding the configured
// duration, invalid UTF-8, evidence/message/event/argv limits, blank tool names,
// and conflicting Command/Argv/Code forms. Errors are not routing decisions;
// the caller chooses its fallback policy. Error text is not a machine-readable API.
//
// History slices must not be mutated concurrently with this call. Empty history
// is valid and uses DefaultTier without consuming a hold, unless compaction
// overrides. The returned state contains no history; supply history on every call.
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

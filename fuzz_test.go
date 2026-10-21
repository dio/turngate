package turngate_test

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	tg "github.com/dio/turngate"
)

// FuzzHistory exercises multiple calls/results, argv, namespaces, malformed state,
// and configuration independently of provider wire formats. JSON is only a corpus
// encoding here, not a request decoder exported by the package.
func FuzzHistory(f *testing.F) {
	seeds := []tg.History{
		{},
		tool("bash", "make", "out of memory", true),
		appendHistory(tool("read", "", "AssertionError", false), tool("bash", "cargo test", "2 passed; 0 failed", false)),
		{Messages: []tg.Message{{Calls: []tg.ToolCall{{ID: "1", Name: "mcp__fs__read"}, {ID: "2", Name: "bash", Argv: []string{"sh", "-c", "cat file"}}}}, {Results: []tg.ToolResult{{CallID: "2", Output: "out of memory"}, {CallID: "1", Output: "AssertionError"}}}}},
		{Compacted: true},
	}
	for _, seed := range seeds {
		data, err := json.Marshal(seed)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data, uint8(2), uint8(3), uint8(50), false)
	}
	f.Fuzz(func(t *testing.T, data []byte, hold, window, threshold uint8, capable bool) {
		if len(data) > 2*tg.MaxEvidenceBytes {
			return
		}
		var history tg.History
		if json.Unmarshal(data, &history) != nil {
			return
		}
		config := tg.DefaultConfig()
		config.RecentWindow = int(window)
		config.ConfidenceThreshold = float64(threshold) / 100
		if capable {
			config.DefaultTier = tg.Capable
		}
		e, err := tg.New(config)
		if window == 0 || threshold > 100 {
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		before, err := json.Marshal(history)
		if err != nil {
			t.Fatal(err)
		}
		state := tg.State{CapableHoldRemaining: uint32(hold)}
		result, next, err := e.Evaluate(history, state)
		if err != nil {
			if next != state || result.Label != "" {
				t.Fatal("invalid evaluation changed state or returned label")
			}
			return
		}
		if hold > 2 {
			t.Fatal("invalid state accepted")
		}
		if result.Label != "efficient" && result.Label != "capable" {
			t.Fatal("unapproved label")
		}
		if next.CapableHoldRemaining > 2 {
			t.Fatal("unbounded hold")
		}
		if result.Score != nil && (math.IsNaN(result.Score.Value) || math.IsInf(result.Score.Value, 0) || math.Abs(result.Score.Value) > 1 || result.Score.Confidence != math.Abs(result.Score.Value)) {
			t.Fatal("invalid score")
		}
		after, err := json.Marshal(history)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("input mutated")
		}
		again, nextAgain, err := e.Evaluate(history, state)
		if err != nil || !reflect.DeepEqual(result, again) || next != nextAgain {
			t.Fatal("non-deterministic evaluation")
		}
	})
}

func FuzzScoreSignals(f *testing.F) {
	f.Add(uint32(0), uint32(0), uint32(0), uint32(0), uint32(0), uint32(0), uint32(0), uint32(0))
	f.Add(math.Float32bits(0.7), uint32(0), uint32(0), uint32(1), uint32(0), uint32(0), uint32(8), uint32(0))
	f.Add(math.Float32bits(float32(math.NaN())), uint32(1), uint32(0), uint32(0), uint32(0), uint32(0), uint32(8), uint32(0))
	f.Add(math.Float32bits(1), ^uint32(0), ^uint32(0), ^uint32(0), ^uint32(0), uint32(0), ^uint32(0), uint32(1))
	f.Fuzz(func(t *testing.T, bits, writes, edits, reads, plans, newActivity, depth, unused uint32) {
		severity := math.Float32frombits(bits)
		signals := tg.Signals{Severity: severity, RecentWrites: writes, RecentEdits: edits, RecentReads: reads, RecentPlans: plans, RecentNew: newActivity, TurnDepth: depth, ToolResultCount: unused}
		score, err := tg.ScoreSignals(signals)
		invalid := math.IsNaN(float64(severity)) || math.IsInf(float64(severity), 0) || severity < 0 || severity > 1
		if invalid {
			if err == nil {
				t.Fatal("invalid severity accepted")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if math.IsNaN(score.Value) || math.IsInf(score.Value, 0) || math.Abs(score.Value) > 1 || score.Confidence != math.Abs(score.Value) || score.ProductionIntensity < 0 || score.ProductionIntensity > 1 {
			t.Fatalf("invalid score: %+v", score)
		}
		if score.Spinning != 0 && score.Exploring != 0 {
			t.Fatal("stall dimensions overlap")
		}
		again, err := tg.ScoreSignals(signals)
		if err != nil || score != again {
			t.Fatal("non-deterministic score")
		}
	})
}

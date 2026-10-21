package turngate_test

import (
	"fmt"

	tg "github.com/dio/turngate"
)

func ExampleEvaluator_Evaluate() {
	config := tg.DefaultConfig()
	config.EfficientLabel = "routine"
	config.CapableLabel = "recovery"
	evaluator, err := tg.New(config)
	if err != nil {
		panic(err)
	}
	history := tg.History{Messages: []tg.Message{
		{Text: "Fix the build."},
		{Calls: []tg.ToolCall{{ID: "build-1", Name: "bash", Command: "make"}}},
		{Results: []tg.ToolResult{{CallID: "build-1", Output: "out of memory", IsError: true}}},
	}}
	result, next, err := evaluator.Evaluate(history, tg.State{})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Label, result.Source, result.OverrideReason)
	fmt.Println(next.CapableHoldRemaining)
	// The consumer maps "recovery" to its approved model/provider routing policy.
	// Output:
	// recovery override critical_error
	// 2
}

func ExampleEvaluator_Evaluate_stateless() {
	config := tg.DefaultConfig()
	config.CapableHoldTurns = 0
	evaluator, err := tg.New(config)
	if err != nil {
		panic(err)
	}
	// The host owns these approved model/provider choices.
	type destination struct{ provider, model string }
	routes := map[string]destination{
		"efficient": {"provider-a", "small-model"},
		"capable":   {"provider-b", "large-model"},
	}
	result, next, err := evaluator.Evaluate(tg.History{}, tg.State{})
	if err != nil {
		panic(err)
	}
	selected := routes[result.Label]
	fmt.Println(selected.provider, selected.model, next.CapableHoldRemaining)
	// Output:
	// provider-a small-model 0
}

func ExampleEvaluator_Evaluate_hold() {
	evaluator, err := tg.New(tg.DefaultConfig())
	if err != nil {
		panic(err)
	}
	// A trusted adapter reports compaction for this evaluation only.
	history := tg.History{Compacted: true}
	result, state, err := evaluator.Evaluate(history, tg.State{})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Source, state.CapableHoldRemaining)

	// On the next logical turn, supply updated history and the saved state.
	// The hold remains even though this turn has no failure evidence.
	history = tg.History{Messages: []tg.Message{
		{Calls: []tg.ToolCall{{ID: "edit-1", Name: "edit"}}},
		{Results: []tg.ToolResult{{CallID: "edit-1", Output: "updated"}}},
	}}
	result, state, err = evaluator.Evaluate(history, state)
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Label, result.Source, state.CapableHoldRemaining)
	// Persist this state once; a transport retry reuses this decision.
	// Output:
	// override 2
	// capable capable_hold 1
}

func ExampleScoreSignals() {
	score, err := tg.ScoreSignals(tg.Signals{RecentWrites: 1})
	if err != nil {
		panic(err)
	}
	fmt.Printf("score=%.3f confidence=%.3f\n", score.Value, score.Confidence)
	// This does not select a label or apply escalation/hold rules.
	// Output:
	// score=-0.874 confidence=0.874
}

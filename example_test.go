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

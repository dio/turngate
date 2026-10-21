package turngate_test

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	tg "github.com/dio/turngate"
)

func evaluator(t testing.TB, change func(*tg.Config)) *tg.Evaluator {
	t.Helper()
	c := tg.DefaultConfig()
	if change != nil {
		change(&c)
	}
	e, err := tg.New(c)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func evaluate(t testing.TB, e *tg.Evaluator, h tg.History, s tg.State) (tg.Result, tg.State) {
	t.Helper()
	r, next, err := e.Evaluate(h, s)
	if err != nil {
		t.Fatal(err)
	}
	return r, next
}

func tool(name, command, output string, failed bool) tg.History {
	return tg.History{Messages: []tg.Message{
		{Calls: []tg.ToolCall{{ID: "1", Name: name, Command: command}}},
		{Results: []tg.ToolResult{{CallID: "1", Output: output, IsError: failed}}},
	}}
}

func appendHistory(a, b tg.History) tg.History {
	return tg.History{Messages: append(append([]tg.Message(nil), a.Messages...), b.Messages...), Compacted: a.Compacted || b.Compacted}
}

func deep(h tg.History) tg.History {
	for len(h.Messages) < 8 {
		h.Messages = append(h.Messages, tg.Message{})
	}
	return h
}

func TestChoices(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		history tg.History
		tier    tg.Tier
		source  tg.Source
		reason  string
	}{
		{"empty", tg.History{}, tg.Efficient, tg.Default, ""},
		{"deep chat abstains", deep(tg.History{}), tg.Efficient, tg.Default, ""},
		{"clean edit", tool("apply_patch", "", "done", false), tg.Efficient, tg.Dimensions, ""},
		{"one hard error is ambiguous", tool("bash", "go test ./...", "AssertionError", false), tg.Efficient, tg.Default, ""},
		{"hard error and spinning", deep(tool("bash", "go test ./...", "AssertionError", false)), tg.Capable, tg.Dimensions, ""},
		{"oom", tool("bash", "make", "out of memory", false), tg.Capable, tg.Override, "critical_error"},
		{"compaction metadata", tg.History{Compacted: true}, tg.Capable, tg.Override, "compaction"},
		{"compaction text", tg.History{Messages: []tg.Message{{Text: "This session is being continued from a previous conversation."}}}, tg.Capable, tg.Override, "compaction"},
		{"hermes compaction", tg.History{Messages: []tg.Message{{Text: "\n [Context compaction — reference only]"}}}, tg.Capable, tg.Override, "compaction"},
		{"tool text is not compaction", tool("custom", "", "session is being continued", false), tg.Efficient, tg.Default, ""},
	}
	e := evaluator(t, func(c *tg.Config) { c.EfficientLabel = "routine"; c.CapableLabel = "complex" })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, next := evaluate(t, e, tt.history, tg.State{})
			if r.Tier != tt.tier || r.Source != tt.source || r.OverrideReason != tt.reason {
				t.Fatalf("got %+v", r)
			}
			want := "routine"
			if tt.tier == tg.Capable {
				want = "complex"
			}
			if r.Label != want {
				t.Fatalf("label %q", r.Label)
			}
			if tt.tier == tg.Capable && next.CapableHoldRemaining != 2 {
				t.Fatalf("hold %+v", next)
			}
			if r.Source == tg.Override && r.Score != nil {
				t.Fatal("override fabricates a score")
			}
		})
	}
}

func TestRetrievalAndDiagnostics(t *testing.T) {
	t.Parallel()
	e := evaluator(t, nil)
	tests := []struct {
		name, tool, command, output string
		failed                      bool
		severity                    float32
		reads                       uint32
	}{
		{"read suppresses quoted failure", "Read", "", "out of memory\nAssertionError", false, 0, 1},
		{"read explicit failure", "Read", "", "connection refused", true, 0.7, 1},
		{"missing read is observation", "read_file", "", "file does not exist", true, 0, 1},
		{"missing execution is hard", "bash", "python missing.py", "no such file or directory", false, 0.7, 0},
		{"grep literal mutation", "bash", "rg 'write_text(' file.py", "out of memory", false, 0, 1},
		{"shell quoted separators", "bash", "grep 'x;y' file", "AssertionError", false, 0, 1},
		{"shell compound reads", "bash", "cat x && head y", "out of memory", false, 0, 1},
		{"shell compound execution", "bash", "cat x && make", "out of memory", false, 1, 1},
		{"shell substitution", "bash", "cat $(make)", "out of memory", false, 1, 1},
		{"shell redirect", "bash", "cat x > y", "out of memory", false, 1, 0},
		{"shell unclosed quote", "bash", "cat 'x", "out of memory", false, 1, 1},
		{"env wrapper", "bash", "env -u DEBUG cat x", "out of memory", false, 0, 1},
		{"shell wrapper", "bash", "sh -c 'cat x'", "out of memory", false, 0, 1},
		{"sed executing script", "bash", "sed -n '1e make' file", "out of memory", false, 1, 1},
		{"sed line range", "bash", "sed -n '1,20p' file", "out of memory", false, 0, 1},
		{"find exec", "bash", "find . -exec make ';'", "out of memory", false, 1, 1},
		{"rg preprocessor", "bash", "rg --pre python x", "out of memory", false, 1, 1},
		{"local cat script", "bash", "./cat x", "out of memory", false, 1, 1},
		{"git read", "bash", "git -C repo status", "out of memory", false, 0, 1},
		{"git validation", "bash", "git diff --check", "out of memory", false, 1, 1},
		{"powershell conservative", "powershell", "cat x", "out of memory", false, 1, 1},
		{"unknown observe retains diagnostics", "lookup", "", "out of memory", false, 1, 0},
		{"codex status success", "bash", "cat x", "Process exited with code 0\nOutput:\nexited with code 1\nout of memory", false, 0, 1},
		{"codex status failure", "bash", "cat x", "Process exited with code 1\nOutput:\nconnection refused", false, 0.7, 1},
		{"hermes status success", "terminal", "cat x", `{"output":"out of memory","exit_code":0}`, false, 0, 1},
		{"hermes status failure", "terminal", "cat x", `{"output":"connection refused","exit_code":1}`, false, 0.7, 1},
		{"zero exit", "bash", "make", "Process exited with code 0", false, 0, 0},
		{"nonzero exit", "bash", "make", "Process exited with code: 2", false, 0.3, 0},
		{"empty explicit error", "bash", "make", "", true, 0.7, 0},
		{"rust compiler", "bash", "cargo build", "error[E0308]: mismatched types", false, 0.7, 0},
		{"js exception trace", "bash", "node app.js", "TypeError: bad\n    at app.js:1", false, 0.7, 0},
		{"js prose alone", "bash", "node app.js", "TypeError: bad", false, 0, 0},
		{"go runtime panic", "bash", "go run .", "panic: runtime error: bad\ngoroutine 1", false, 0.7, 0},
		{"patch failure", "apply_patch", "", "Invalid context 42", false, 0.7, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := evaluate(t, e, tool(tt.tool, tt.command, tt.output, tt.failed), tg.State{})
			if r.Signals.Severity != tt.severity || r.Signals.RecentReads != tt.reads {
				t.Fatalf("signals %+v; severity %v reads %d wanted", r.Signals, tt.severity, tt.reads)
			}
		})
	}
}

func TestWindowsAndRepeatedFailures(t *testing.T) {
	t.Parallel()
	e := evaluator(t, nil)
	errorA := tool("bash", "make", "/tmp/a.go:10: error: patch failed: bad hunk 12", false)
	errorB := tool("bash", "make", "/tmp/b.go:22: error: patch failed: bad hunk 98", false)
	// Use a diagnostic recognized at line start, keeping changing paths in its text.
	errorA.Messages[1].Results[0].Output = "error: patch failed: /tmp/a.go hunk 12"
	errorB.Messages[1].Results[0].Output = "error: patch failed: /tmp/b.go hunk 98"
	h := appendHistory(errorA, errorB)
	r, _ := evaluate(t, e, h, tg.State{})
	if !r.Signals.RepeatedFailure || r.OverrideReason != "repeated_failure" {
		t.Fatalf("repeated %+v", r)
	}
	clean := tool("write", "", "done", false)
	h = appendHistory(h, clean)
	r, _ = evaluate(t, e, h, tg.State{})
	if !r.Signals.RepeatedFailure {
		t.Fatal("failure must persist within window")
	}
	h = appendHistory(h, clean)
	r, _ = evaluate(t, e, h, tg.State{})
	if r.Signals.RepeatedFailure || r.Signals.Severity != 0.7 {
		t.Fatalf("one remaining failure %+v", r.Signals)
	}
	h = appendHistory(h, clean)
	r, _ = evaluate(t, e, h, tg.State{})
	if r.Signals.Severity != 0 || r.Signals.RecentWrites != 3 {
		t.Fatalf("expired window %+v", r.Signals)
	}
	soft := appendHistory(tool("bash", "make", "exit code 1", false), tool("bash", "make", "exit code 2", false))
	r, _ = evaluate(t, e, soft, tg.State{})
	if r.Signals.RepeatedFailure {
		t.Fatal("soft exits should not repeat")
	}
	distinct := appendHistory(tool("bash", "make", "AssertionError: wrong type", false), tool("bash", "make", "AssertionError: missing field", false))
	r, _ = evaluate(t, e, distinct, tg.State{})
	if r.Signals.RepeatedFailure {
		t.Fatal("different diagnostic conflated")
	}
}

func TestHoldTransitions(t *testing.T) {
	t.Parallel()
	e := evaluator(t, nil)
	_, state := evaluate(t, e, tool("bash", "make", "out of memory", false), tg.State{})
	clean := tool("write", "", "done", false)
	for i := uint32(2); i > 0; i-- {
		r, next := evaluate(t, e, clean, state)
		if r.Source != tg.CapableHold || next.CapableHoldRemaining != i-1 {
			t.Fatalf("hold %d: %+v %+v", i, r, next)
		}
		state = next
	}
	r, state := evaluate(t, e, clean, state)
	if r.Tier != tg.Efficient || state.CapableHoldRemaining != 0 {
		t.Fatal("hold did not expire")
	}
	r, state = evaluate(t, e, tool("bash", "go test", "\nok example/package 0.1s", false), tg.State{CapableHoldRemaining: 2})
	if r.Source == tg.CapableHold || state.CapableHoldRemaining != 0 {
		t.Fatalf("test did not clear %+v %+v", r, state)
	}
	r, state = evaluate(t, e, tg.History{}, tg.State{CapableHoldRemaining: 2})
	if r.Source != tg.Default || state.CapableHoldRemaining != 2 {
		t.Fatal("absent history consumed hold")
	}
	// A fresh override while held consumes one hold turn; it does not extend it.
	r, state = evaluate(t, e, tool("bash", "make", "out of memory", false), tg.State{CapableHoldRemaining: 2})
	if r.Source != tg.CapableHold || state.CapableHoldRemaining != 1 {
		t.Fatal("hold precedence changed")
	}
	nohold := evaluator(t, func(c *tg.Config) { c.CapableHoldTurns = 0 })
	_, state = evaluate(t, nohold, tool("bash", "make", "out of memory", false), tg.State{})
	if state.CapableHoldRemaining != 0 {
		t.Fatal("disabled hold")
	}
	capable := evaluator(t, func(c *tg.Config) { c.DefaultTier = tg.Capable })
	r, state = evaluate(t, capable, tg.History{}, tg.State{})
	if r.Tier != tg.Capable || state.CapableHoldRemaining != 0 {
		t.Fatal("default should not start a hold")
	}
}

func TestTestPassDetection(t *testing.T) {
	t.Parallel()
	e := evaluator(t, nil)
	for _, tt := range []struct {
		name, output string
		want         bool
	}{
		{"cargo", "test result: ok. 2 passed; 0 failed", true},
		{"mixed failures", "2 passed; 1 failed", false},
		{"multiline failure count", "2 passed; 1\nfailed", false},
		{"literal error", "2 passed; error: bad", false},
		{"zero errors", "2 passed; 0 errors", true},
		{"no test", "done", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := evaluate(t, e, tool("bash", "cargo test", tt.output, false), tg.State{})
			if r.Signals.TestsPassed != tt.want {
				t.Fatalf("%+v", r.Signals)
			}
		})
	}
	h := appendHistory(tool("bash", "cargo test", "2 passed", false), tool("bash", "cargo test", "exit code 1", false))
	r, _ := evaluate(t, e, h, tg.State{})
	if r.Signals.TestsPassed {
		t.Fatal("old pass survived newer failure")
	}
	h = appendHistory(h, tool("bash", "cargo test", "2 passed", false))
	r, _ = evaluate(t, e, h, tg.State{})
	if !r.Signals.TestsPassed {
		t.Fatal("new pass was lost")
	}
	r, _ = evaluate(t, e, tool("read", "", "all tests passed", false), tg.State{CapableHoldRemaining: 1})
	if r.Source != tg.CapableHold {
		t.Fatal("retrieved test text cleared hold")
	}
}

func TestToolNamesAndCommandForms(t *testing.T) {
	t.Parallel()
	e := evaluator(t, func(c *tg.Config) {
		c.ToolSemantics = map[string]tg.Semantic{"lookup": tg.Observe, "save": tg.Mutate, "plan_task": tg.Plan, "send": tg.NewActivity}
	})
	for _, tt := range []struct {
		name                             string
		call                             tg.ToolCall
		writes, edits, reads, plans, new uint32
	}{
		{"mcp bare", tg.ToolCall{Name: "mcp__db__save"}, 1, 0, 0, 0, 0},
		{"explicit bare", tg.ToolCall{Name: "custom__lookup", BareName: "lookup"}, 0, 0, 1, 0, 0},
		{"case insensitive", tg.ToolCall{Name: "SAVE"}, 1, 0, 0, 0, 0},
		{"plan", tg.ToolCall{Name: "plan_task"}, 0, 0, 0, 1, 0},
		{"new", tg.ToolCall{Name: "send"}, 0, 0, 0, 0, 1},
		{"argv retrieval", tg.ToolCall{Name: "bash", Argv: []string{"cat", "file"}}, 0, 0, 1, 0, 0},
		{"editor view", tg.ToolCall{Name: "text_editor", Command: "view"}, 0, 0, 1, 0, 0},
		{"mkdir", tg.ToolCall{Name: "bash", Command: "mkdir -p dir"}, 1, 0, 0, 0, 0},
		{"gofmt", tg.ToolCall{Name: "bash", Command: "gofmt -w file.go"}, 0, 1, 0, 0, 0},
		{"python write", tg.ToolCall{Name: "bash", Command: "python -c 'p.write_text(x)'"}, 1, 0, 0, 0, 0},
		{"node write", tg.ToolCall{Name: "bash", Command: "node -e 'fs.writeFileSync(x,y)'"}, 1, 0, 0, 0, 0},
		{"opaque code", tg.ToolCall{Name: "exec", Code: "await tools.apply_patch(patch)"}, 0, 0, 0, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := evaluate(t, e, tg.History{Messages: []tg.Message{{Calls: []tg.ToolCall{tt.call}}}}, tg.State{})
			s := r.Signals
			if s.RecentWrites != tt.writes || s.RecentEdits != tt.edits || s.RecentReads != tt.reads || s.RecentPlans != tt.plans || s.RecentNew != tt.new {
				t.Fatalf("%+v", s)
			}
		})
	}
	r, _ := evaluate(t, e, tool("lookup", "", "out of memory", false), tg.State{})
	if r.Signals.Severity != 1 {
		t.Fatal("custom observation must not suppress diagnostics")
	}
}

func TestResultLinkageAndEmptyOutputs(t *testing.T) {
	t.Parallel()
	e := evaluator(t, nil)
	h := appendHistory(tool("read", "", "safe", false), tool("bash", "make", "out of memory", false))
	r, _ := evaluate(t, e, h, tg.State{})
	if r.Signals.Severity != 1 {
		t.Fatal("reused ID used stale retrieval semantics")
	}
	h = tg.History{Messages: []tg.Message{{Results: []tg.ToolResult{{CallID: "missing", Output: "out of memory"}}}}}
	r, _ = evaluate(t, e, h, tg.State{})
	if r.Signals.Severity != 1 {
		t.Fatal("orphan result suppressed")
	}
	h = tool("bash", "make", "AssertionError", false)
	for range 3 {
		h = appendHistory(h, tool("write", "", "", false))
	}
	r, _ = evaluate(t, e, h, tg.State{})
	if r.Signals.Severity != 0.7 || r.Signals.ToolResultCount != 4 {
		t.Fatalf("empty result contract %+v", r.Signals)
	}
	for range 3 {
		h = appendHistory(h, tool("read", "", "retrieved content", false))
	}
	r, _ = evaluate(t, e, h, tg.State{})
	if r.Signals.Severity != 0 {
		t.Fatal("retrieval placeholders must advance result window")
	}
}

func TestConfigurationAndBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*tg.Config)
	}{
		{"nan", func(c *tg.Config) { c.ConfidenceThreshold = math.NaN() }},
		{"infinite", func(c *tg.Config) { c.ConfidenceThreshold = math.Inf(1) }},
		{"negative", func(c *tg.Config) { c.ConfidenceThreshold = -1 }},
		{"large", func(c *tg.Config) { c.ConfidenceThreshold = 1.1 }},
		{"zero window", func(c *tg.Config) { c.RecentWindow = 0 }},
		{"large window", func(c *tg.Config) { c.RecentWindow = tg.MaxToolEvents + 1 }},
		{"large hold", func(c *tg.Config) { c.CapableHoldTurns = tg.MaxToolEvents + 1 }},
		{"empty label", func(c *tg.Config) { c.EfficientLabel = " " }},
		{"duplicate labels", func(c *tg.Config) { c.EfficientLabel = c.CapableLabel }},
		{"long label", func(c *tg.Config) { c.EfficientLabel = strings.Repeat("x", 129) }},
		{"invalid tier", func(c *tg.Config) { c.DefaultTier = "other" }},
		{"built in reclassification", func(c *tg.Config) { c.ToolSemantics = map[string]tg.Semantic{"READ": tg.Mutate} }},
		{"duplicate semantic", func(c *tg.Config) { c.ToolSemantics = map[string]tg.Semantic{"foo": tg.Mutate, "FOO": tg.Observe} }},
		{"unknown semantic", func(c *tg.Config) { c.ToolSemantics = map[string]tg.Semantic{"foo": "other"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tg.DefaultConfig()
			tt.change(&c)
			if _, err := tg.New(c); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	e := evaluator(t, nil)
	bad := []struct {
		name string
		h    tg.History
	}{
		{"size", tg.History{Messages: []tg.Message{{Text: strings.Repeat("x", tg.MaxEvidenceBytes+1)}}}},
		{"messages", tg.History{Messages: make([]tg.Message, tg.MaxMessages+1)}},
		{"events", tg.History{Messages: []tg.Message{{Results: make([]tg.ToolResult, tg.MaxToolEvents+1)}}}},
		{"utf8", tg.History{Messages: []tg.Message{{Text: string([]byte{255})}}}},
		{"ambiguous command", tg.History{Messages: []tg.Message{{Calls: []tg.ToolCall{{Name: "bash", Command: "cat", Argv: []string{"cat"}}}}}}},
		{"blank tool", tg.History{Messages: []tg.Message{{Calls: []tg.ToolCall{{Name: " "}}}}}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			state := tg.State{CapableHoldRemaining: 1}
			r, next, err := e.Evaluate(tt.h, state)
			if err == nil || next != state || r.Label != "" {
				t.Fatalf("%+v %+v %v", r, next, err)
			}
		})
	}
	state := tg.State{CapableHoldRemaining: 3}
	_, next, err := e.Evaluate(tg.History{}, state)
	if err == nil || next != state {
		t.Fatal("invalid state accepted or changed")
	}
	for _, invalid := range []*tg.Evaluator{nil, {}} {
		if _, _, err := invalid.Evaluate(tg.History{}, tg.State{}); err == nil {
			t.Fatal("zero evaluator accepted")
		}
	}
	_, _ = evaluate(t, e, tg.History{Messages: []tg.Message{{Text: strings.Repeat("x", tg.MaxEvidenceBytes)}}}, tg.State{})
}

func TestThresholds(t *testing.T) {
	t.Parallel()
	h := tool("bash", "make", "AssertionError", false)
	base := evaluator(t, nil)
	r, _ := evaluate(t, base, h, tg.State{})
	at := evaluator(t, func(c *tg.Config) { c.ConfidenceThreshold = r.Score.Confidence })
	exact, _ := evaluate(t, at, h, tg.State{})
	if exact.Source != tg.Default {
		t.Fatal("exact threshold must be ambiguous")
	}
	zero := evaluator(t, func(c *tg.Config) { c.ConfidenceThreshold = 0 })
	low, _ := evaluate(t, zero, h, tg.State{})
	if low.Source != tg.Dimensions || low.Tier != tg.Capable {
		t.Fatal("zero threshold")
	}
	one := evaluator(t, func(c *tg.Config) { c.ConfidenceThreshold = 1 })
	high, _ := evaluate(t, one, deep(h), tg.State{})
	if high.Source != tg.Default {
		t.Fatal("one threshold")
	}
	high, _ = evaluate(t, one, tool("bash", "make", "out of memory", false), tg.State{})
	if high.Source != tg.Override {
		t.Fatal("threshold suppressed override")
	}
}

func TestImmutableConcurrentEvaluations(t *testing.T) {
	c := tg.DefaultConfig()
	c.ToolSemantics = map[string]tg.Semantic{"save": tg.Mutate}
	e, err := tg.New(c)
	if err != nil {
		t.Fatal(err)
	}
	c.ToolSemantics["save"] = tg.Observe
	h := tool("save", "", "done", false)
	before, _ := json.Marshal(h)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, next, err := e.Evaluate(h, tg.State{})
			if err != nil || r.Source != tg.Dimensions || r.Signals.RecentWrites != 1 || next.CapableHoldRemaining != 0 {
				t.Errorf("%+v %+v %v", r, next, err)
			}
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(h)
	if string(before) != string(after) {
		t.Fatal("history mutated")
	}
}

func TestReferenceScores(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/scores.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string
		Signals tg.Signals
		Value   float64
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			got, err := tg.ScoreSignals(tt.Signals)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got.Value-tt.Value) > 1e-14 {
				t.Fatalf("got %.17g want %.17g", got.Value, tt.Value)
			}
			if got.Confidence != math.Abs(got.Value) {
				t.Fatal("confidence")
			}
		})
	}
	for _, severity := range []float32{-1, 2, float32(math.NaN()), float32(math.Inf(1))} {
		if _, err := tg.ScoreSignals(tg.Signals{Severity: severity}); err == nil {
			t.Fatal("invalid severity")
		}
	}
}

func FuzzEvaluate(f *testing.F) {
	f.Add("bash", "make", "out of memory", false, uint8(2))
	f.Add("read", "", "all tests passed", false, uint8(0))
	f.Add("bash", "cat $(make)", "AssertionError", true, uint8(8))
	e := evaluator(f, nil)
	f.Fuzz(func(t *testing.T, name, command, output string, failed bool, depth uint8) {
		h := tool(name, command, output, failed)
		for len(h.Messages) < int(depth%16) {
			h.Messages = append(h.Messages, tg.Message{})
		}
		previous := tg.State{CapableHoldRemaining: uint32(depth % 3)}
		r, next, err := e.Evaluate(h, previous)
		if err != nil {
			if next != previous || r.Label != "" {
				t.Fatal("error changed state")
			}
			return
		}
		if r.Label != "efficient" && r.Label != "capable" {
			t.Fatal("unknown label")
		}
		if next.CapableHoldRemaining > 2 {
			t.Fatal("unbounded hold")
		}
		if r.Score != nil && (math.IsNaN(r.Score.Value) || math.IsInf(r.Score.Value, 0) || r.Score.Value < -1 || r.Score.Value > 1 || r.Score.Confidence != math.Abs(r.Score.Value)) {
			t.Fatal("invalid score")
		}
		again, state, err := e.Evaluate(h, previous)
		if err != nil || !reflect.DeepEqual(r, again) || state != next {
			t.Fatal("not deterministic")
		}
	})
}

func TestShellRecognitionBounds(t *testing.T) {
	t.Parallel()
	e := evaluator(t, nil)
	tests := []struct {
		name string
		call tg.ToolCall
	}{
		{"large command", tg.ToolCall{ID: "1", Name: "bash", Command: "cat " + strings.Repeat("x", 16*1024)}},
		{"large argv", tg.ToolCall{ID: "1", Name: "bash", Argv: []string{"cat", strings.Repeat("x", 16*1024)}}},
		{"deep wrappers", tg.ToolCall{ID: "1", Name: "bash", Command: strings.Repeat("command ", 22) + "cat file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tg.History{Messages: []tg.Message{{Calls: []tg.ToolCall{tt.call}}, {Results: []tg.ToolResult{{CallID: "1", Output: "out of memory"}}}}}
			r, _ := evaluate(t, e, h, tg.State{})
			if r.Signals.Severity != 1 {
				t.Fatal("unrecognized command suppressed diagnostic")
			}
		})
	}
}

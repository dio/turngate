// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
// SPDX-License-Identifier: Apache-2.0
// Adapted from Switchyard tool_signals.rs; see NOTICE.
package turngate

import "strings"

type activity uint8

const (
	unknown activity = iota
	read
	write
	edit
	planning
	forward
)

var (
	writeNames = []string{"write", "create_file", "new_file", "write_file"}
	editNames  = []string{"edit", "multiedit", "notebookedit", "str_replace", "str_replace_based_edit_tool", "apply_patch", "text_editor", "patch"}
	readNames  = []string{"read", "view", "read_file", "search_files", "glob", "grep", "find", "ls"}
	planNames  = []string{"todowrite", "todo_write", "todo", "update_plan", "todo_list"}
	shellNames = []string{"bash", "shell_command", "shell", "local_shell_call", "terminal", "exec_command", "exec", "powershell"}
)

func asciiLower(s string) string {
	return strings.Map(func(c rune) rune {
		if c >= 'A' && c <= 'Z' {
			return c + 32
		}
		return c
	}, s)
}

func builtin(name string) bool {
	return containsString(writeNames, name) || containsString(editNames, name) || containsString(readNames, name) || containsString(planNames, name) || containsString(shellNames, name)
}

func editor(name string) bool { return name == "text_editor" || name == "str_replace_based_edit_tool" }

func bareName(c ToolCall) string {
	if c.BareName != "" {
		return c.BareName
	}
	if tail, ok := strings.CutPrefix(c.Name, "mcp__"); ok {
		_, tool, ok := strings.Cut(tail, "__")
		if ok {
			return tool
		}
	}
	return ""
}

func (e *Evaluator) classify(name, command string) activity {
	lower := strings.ToLower(name)
	switch {
	case containsString(writeNames, lower):
		return write
	case editor(lower) && command == "view":
		return read
	case containsString(editNames, lower):
		return edit
	case containsString(readNames, lower):
		return read
	case containsString(planNames, lower):
		return planning
	case containsString(shellNames, lower):
		if a := shellActivity(command); a != unknown {
			return a
		}
	}
	switch e.config.ToolSemantics[asciiLower(name)] {
	case Observe:
		return read
	case Mutate:
		return write
	case Plan:
		return planning
	case NewActivity:
		return forward
	}
	return unknown
}

func retrieval(name string, c ToolCall) bool {
	lower := strings.ToLower(name)
	if containsString(readNames, lower) {
		return true
	}
	if editor(lower) && strings.EqualFold(c.Command, "view") {
		return true
	}
	if lower == "powershell" || !containsString(shellNames, lower) {
		return false
	}
	if len(c.Argv) > 0 {
		size := 0
		for _, word := range c.Argv {
			size += len(word) + 1
		}
		if size > 16*1024 {
			return false
		}
		return retrievalCommand(c.Argv, 0)
	}
	return shellRetrieval(c.Command, 0)
}

func (e *Evaluator) observe(c ToolCall) (activity, bool) {
	if c.Code != "" {
		return unknown, false
	}
	command := c.Command
	if len(c.Argv) > 0 {
		command = strings.Join(c.Argv, " ")
	}
	command = strings.ToLower(command)
	full := e.classify(c.Name, command)
	bare := bareName(c)
	name := c.Name
	if (full == unknown || full == read) && bare != "" {
		name = bare
	}
	retrieved := retrieval(name, c)
	if retrieved {
		return read, true
	}
	if full == unknown && bare != "" {
		full = e.classify(bare, command)
	}
	return full, false
}

type observedResult struct {
	text     string
	failed   bool
	severity float32
}

func (e *Evaluator) extract(h History) Signals {
	s := Signals{TurnDepth: uint32(len(h.Messages)), Compacted: h.Compacted}
	var calls []activity
	var results []observedResult
	retrievalIDs := make(map[string]bool)
	for _, m := range h.Messages {
		lower := strings.ToLower(m.Text)
		if strings.Contains(lower, "session is being continued") {
			s.Compacted = true
		}
		for _, line := range strings.Split(lower, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[context compaction — reference only]") || strings.HasPrefix(line, "[still in progress — this is the active request, restated after the compaction boundary") {
				s.Compacted = true
			}
		}
		for _, c := range m.Calls {
			a, retrieved := e.observe(c)
			calls = append(calls, a)
			if c.ID != "" {
				retrievalIDs[c.ID] = retrieved
			}
		}
		for _, r := range m.Results {
			s.ToolResultCount++
			if retrievalIDs[r.CallID] && (missingFile(r.Output) || (!r.IsError && !retrievalFailed(r.Output))) {
				if r.Output != "" {
					results = append(results, observedResult{})
				}
				continue
			}
			if r.Output != "" || r.IsError {
				sev, _ := classifyText(r.Output)
				if r.IsError {
					sev = max(sev, hard)
				}
				results = append(results, observedResult{r.Output, r.IsError, sev})
			}
		}
	}
	s.ToolCallCount = uint32(len(calls))
	for _, a := range calls[max(0, len(calls)-e.config.RecentWindow):] {
		switch a {
		case write:
			s.RecentWrites++
		case edit:
			s.RecentEdits++
		case read:
			s.RecentReads++
		case planning:
			s.RecentPlans++
		case forward:
			s.RecentNew++
		}
	}
	recent := results[max(0, len(results)-e.config.RecentWindow):]
	fingerprints := make(map[string]bool)
	for _, r := range recent {
		s.Severity = max(s.Severity, r.severity)
		if fp := failureFingerprint(r.text, r.failed); fp != "" {
			s.RepeatedFailure = s.RepeatedFailure || fingerprints[fp]
			fingerprints[fp] = true
		}
	}
	for i := len(results) - 1; i >= 0; i-- {
		if results[i].severity > 0 || results[i].failed {
			break
		}
		s.NoErrorStreak++
	}
	for _, r := range recent {
		if r.severity > 0 || r.failed {
			s.TestsPassed = false
		} else if testPass(r.text) {
			s.TestsPassed = true
		}
	}
	return s
}

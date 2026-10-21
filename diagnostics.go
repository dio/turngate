// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
// SPDX-License-Identifier: Apache-2.0
// Adapted from Switchyard tool_signals.rs; see NOTICE.
package turngate

import (
	"encoding/json"
	"strings"
	"unicode"
)

const (
	soft     float32 = 0.3
	hard     float32 = 0.7
	critical float32 = 1
)

type errorPattern struct {
	name      string
	severity  float32
	fragments []string
}

var errorPatterns = []errorPattern{
	{"oom", critical, []string{"out of memory", "memoryerror", "cannot allocate memory"}},
	{"connection_refused", hard, []string{"connection refused", "connectionrefusederror", "econnrefused"}},
	{"traceback", hard, []string{"traceback (most recent call last)"}},
	{"import_error", hard, []string{"modulenotfounderror:", "importerror:", "no module named "}},
	{"cmd_not_found", hard, []string{"command not found", "not found\n", "/usr/bin/env: "}},
	{"assertion", hard, []string{"assertionerror"}},
	{"value_error", hard, []string{"valueerror:"}},
	{"syntax_error", hard, []string{"syntaxerror:"}},
	{"timeout", hard, []string{"timed out", "timeouterror", "timeout expired", "deadline exceeded"}},
	{"no_such_file", hard, []string{"filenotfounderror:", "no such file or directory", "file does not exist"}},
	{"exit_nonzero", soft, []string{"returned non-zero"}},
}

var (
	exitPhrases = []string{"exit code", "exit status", "exited with code", "exited with status"}
	passPhrases = []string{" passed", "passed in", "tests passed", "all tests passed", "test ok", "test result: ok", "passed.\n", "tests pass", "\nok ", "✓ "}
)

type structuredOutput struct {
	Output   *string `json:"output"`
	ExitCode *int64  `json:"exit_code"`
	Success  *bool   `json:"success"`
	Error    string  `json:"error"`
}

func structured(text string) structuredOutput {
	var v structuredOutput
	// A harness may append a warning after the JSON object. Match the first value.
	if err := json.NewDecoder(strings.NewReader(text)).Decode(&v); err != nil {
		return structuredOutput{}
	}
	return v
}

func (v structuredOutput) failed() bool {
	return (v.ExitCode != nil && *v.ExitCode != 0) || (v.Success != nil && !*v.Success) || strings.TrimSpace(v.Error) != ""
}

func containsAny(s string, fragments []string) bool {
	for _, p := range fragments {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func nonzeroDigits(s string) bool {
	for _, c := range s {
		if c != '0' {
			return true
		}
	}
	return false
}
func digit(c byte) bool      { return c >= '0' && c <= '9' }
func asciiAlnum(c byte) bool { return digit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func nonzeroExit(lower string) bool {
	for _, phrase := range exitPhrases {
		rest := lower
		for {
			at := strings.Index(rest, phrase)
			if at < 0 {
				break
			}
			rest = rest[at+len(phrase):]
			value := strings.TrimLeft(rest, " \t\n\r\v\f:='\"`")
			end := 0
			for end < len(value) && digit(value[end]) {
				end++
			}
			if end > 0 && nonzeroDigits(value[:end]) {
				return true
			}
		}
	}
	return false
}

func classifyText(text string) (float32, []string) {
	lower := strings.ToLower(text)
	severity := float32(0)
	var patterns []string
	add := func(name string, level float32) { patterns = append(patterns, name); severity = max(severity, level) }
	for _, p := range errorPatterns {
		if containsAny(lower, p.fragments) {
			add(p.name, p.severity)
		}
	}
	v := structured(text)
	if (v.ExitCode != nil && *v.ExitCode != 0) || nonzeroExit(lower) {
		if !containsString(patterns, "exit_nonzero") {
			add("exit_nonzero", soft)
		}
	}
	if (v.Success != nil && !*v.Success) || strings.TrimSpace(v.Error) != "" {
		add("tool_error", hard)
	}
	compile, exception, panicLine, patch := false, false, false, false
	for _, line := range strings.Split(lower, "\n") {
		line = strings.TrimLeftFunc(line, unicode.IsSpace)
		if line == "compilation failed" || line == "error: compilation failed" || line == "error: could not compile" || strings.HasPrefix(line, "error: could not compile ") {
			compile = true
		}
		if rest, ok := strings.CutPrefix(line, "error[e"); ok {
			if code, _, ok := strings.Cut(rest, "]:"); ok && code != "" {
				valid := true
				for i := range len(code) {
					valid = valid && digit(code[i])
				}
				compile = compile || valid
			}
		}
		for _, prefix := range []string{"typeerror:", "referenceerror:", "rangeerror:", "runtimeerror:", "keyerror:", "attributeerror:"} {
			exception = exception || strings.HasPrefix(line, prefix)
		}
		panicLine = panicLine || strings.HasPrefix(line, "panic: runtime error:")
		patch = patch || strings.HasPrefix(line, "error: patch failed:") || strings.HasPrefix(line, "patch failed:") || strings.Contains(line, ": patch does not apply") || strings.HasPrefix(line, "invalid context")
	}
	if compile {
		add("compile_error", hard)
	}
	if exception && (strings.Contains(lower, "\n    at ") || strings.Contains(lower, "\n  at ")) {
		add("runtime_exception", hard)
	}
	if panicLine && (strings.Contains(lower, "\ngoroutine ") || strings.Contains(lower, "[signal sig")) {
		add("runtime_panic", hard)
	}
	if patch {
		add("patch_error", hard)
	}
	return severity, patterns
}

func containsString(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

func missingFile(text string) bool {
	return containsAny(strings.ToLower(text), []string{"no such file or directory", "file does not exist"})
}

func retrievalFailed(text string) bool {
	v := structured(text)
	if v.Output != nil && v.ExitCode != nil {
		return v.failed()
	}
	header, _, _ := strings.Cut(text, "\nOutput:\n")
	return nonzeroExit(strings.ToLower(header))
}

func failureFingerprint(text string, isError bool) string {
	level, patterns := classifyText(text)
	if level < hard && !isError {
		return ""
	}
	lower := strings.ToLower(text)
	diagnostic := ""
	for _, line := range strings.Split(lower, "\n") {
		if containsAny(line, []string{"error", "exception", "panic", "failed", "timed out", "timeout", "connection refused", "cannot allocate memory", "out of memory", "not found"}) {
			diagnostic = line
			break
		}
	}
	if diagnostic == "" {
		for _, line := range strings.Split(lower, "\n") {
			if strings.TrimSpace(line) != "" {
				diagnostic = line
				break
			}
		}
	}
	words := strings.Fields(diagnostic)
	for i, word := range words {
		if strings.HasPrefix(word, "/") || strings.Contains(word, "/src/") || strings.Contains(word, "/tmp/") {
			words[i] = "<path>"
			continue
		}
		var b strings.Builder
		inDigits := false
		for _, c := range word {
			if c >= '0' && c <= '9' {
				if !inDigits {
					b.WriteByte('#')
				}
				inDigits = true
			} else {
				b.WriteRune(c)
				inDigits = false
			}
		}
		words[i] = b.String()
	}
	normalized := []rune(strings.Join(words, " "))
	if len(normalized) > 240 {
		normalized = normalized[:240]
	}
	return strings.Join(patterns, ",") + "|" + string(normalized)
}

func testPass(text string) bool {
	lower := strings.ToLower(text)
	if !containsAny(lower, passPhrases) || containsAny(lower, []string{"✗ ", "fatal:", "assertionerror", "error:"}) {
		return false
	}
	for _, kw := range []string{"failed", "failure", "failures", "errors", "error"} {
		offset := 0
		for {
			at := strings.Index(lower[offset:], kw)
			if at < 0 {
				break
			}
			at += offset
			end := at + len(kw)
			if end == len(lower) || !asciiAlnum(lower[end]) {
				prefix := strings.TrimRightFunc(lower[:at], unicode.IsSpace)
				start := len(prefix)
				for start > 0 && digit(prefix[start-1]) {
					start--
				}
				if start < len(prefix) && nonzeroDigits(prefix[start:]) {
					return false
				}
			}
			offset = end
		}
	}
	return true
}

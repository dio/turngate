// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
// SPDX-License-Identifier: Apache-2.0
// Adapted from Switchyard tool_signals.rs; see NOTICE.

package turngate

import (
	"path"
	"strings"
)

var (
	writePatterns = []string{"cat >", "cat >>", "echo >", "echo >>", "tee ", "printf >", "printf >>", "> /", ">> /", "<< 'eof'", "<<eof", "<<'eof'", "<< eof"}
	editPatterns  = []string{"sed -i", "sed --in-place", "awk -i inplace", "awk 'inplace=1'", "patch ", "patch -p", "perl -i", "perl -p -i", "perl -pi"}
	readPatterns  = []string{"cat /", "cat ./", "cat ../", "grep ", "ls ", "ls -", "find ", "head ", "tail ", "wc ", "diff ", "which ", "ps ", "df ", "du ", "stat ", "file ", "less ", "more "}
	readCommands  = []string{"cat", "rg", "nl", "jq", "pwd", "tree", "sed", "grep", "ls", "find", "head", "tail", "wc", "diff", "which", "ps", "df", "du", "stat", "file", "less", "more", "readlink", "realpath", "basename", "dirname", "printenv"}
	gitReads      = []string{"status", "diff", "log", "show", "show-ref", "rev-parse", "ls-files", "ls-remote", "ls-tree", "grep", "blame", "merge-base", "check-ignore", "tag"}
)

// shellSegments respects quotes and escapes when splitting command separators.
// It is not a shell interpreter and never executes anything.
func shellSegments(command string) []string {
	var segments []string
	start := 0
	var quote rune
	escaped := false
	for i, c := range command {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		around := quote
		if quote == c {
			quote = 0
		} else if quote == 0 && (c == '\'' || c == '"') {
			quote = c
		}
		if around == 0 && strings.ContainsRune("\n;|&", c) {
			if part := strings.TrimSpace(command[start:i]); part != "" {
				segments = append(segments, part)
			}
			start = i + 1
		}
	}
	if part := strings.TrimSpace(command[start:]); part != "" {
		segments = append(segments, part)
	}
	return segments
}

func shellWords(segment string) []string {
	words := strings.Fields(segment)
	if len(words) > 0 && words[0] == "env" {
		words = words[1:]
		for len(words) > 0 && strings.HasPrefix(words[0], "-") {
			words = words[1:]
		}
	}
	for len(words) > 0 && strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "=") {
		words = words[1:]
	}
	return words
}

func shellActivity(command string) activity {
	if command == "" {
		return unknown
	}
	segments := shellSegments(command)
	writeFound, editFound, readFound, node := false, false, false, false
	for _, segment := range segments {
		words := shellWords(segment)
		if len(words) == 0 {
			if segment == "env" {
				readFound = true
			}
			continue
		}
		program := path.Base(words[0])
		args := words[1:]
		has := func(s string) bool { return containsString(args, s) }
		node = node || program == "node"
		if containsString([]string{"cp", "mkdir", "touch", "install"}, program) {
			writeFound = true
		}
		if (has(">") || has(">>")) && (containsString([]string{"echo", "printf", "git"}, program) || containsString(readCommands, program)) {
			writeFound = true
		}
		switch program {
		case "mv", "rm":
			editFound = true
		case "perl":
			for _, a := range args {
				if !strings.HasPrefix(a, "-") {
					break
				}
				if strings.Contains(strings.TrimLeft(a, "-"), "i") {
					editFound = true
				}
			}
		case "git":
			if len(args) > 0 {
				switch args[0] {
				case "apply", "am", "restore":
					editFound = true
				case "branch":
					readFound = readFound || len(args) == 1 || strings.HasPrefix(args[1], "-")
				case "remote":
					readFound = readFound || len(args) == 1 || strings.HasPrefix(args[1], "-") || args[1] == "get-url"
				case "config":
					readFound = readFound || (len(args) > 1 && containsString([]string{"--get", "--get-all", "--list", "-l"}, args[1]))
				default:
					readFound = readFound || containsString(gitReads, args[0])
				}
			}
		case "gofmt":
			editFound = editFound || has("-w")
		case "cargo":
			editFound = editFound || (len(args) > 0 && args[0] == "fmt" && !has("--check"))
		case "ruff":
			editFound = editFound || (len(args) > 0 && ((args[0] == "format" && !has("--check")) || (args[0] == "check" && has("--fix"))))
		case "prettier":
			editFound = editFound || has("--write")
		case "black":
			editFound = editFound || !has("--check")
		default:
			for _, a := range args {
				if path.Base(a) == "prettier" && has("--write") {
					editFound = true
				}
				if path.Base(a) == "ruff" && ((has("format") && !has("--check")) || (has("check") && has("--fix"))) {
					editFound = true
				}
			}
		}
		readFound = readFound || containsString(readCommands, program) || program == "type" || (program == "command" && len(args) > 0 && args[0] == "-v")
	}
	if writeFound || containsAny(command, writePatterns) || (strings.Contains(command, "python") && containsAny(command, []string{"write_text(", "writelines(", ".write("})) || (node && containsAny(command, []string{"writefilesync(", "writefile(", "appendfilesync(", "appendfile("})) {
		return write
	}
	if editFound || containsAny(command, editPatterns) {
		return edit
	}
	if readFound || containsAny(command, readPatterns) {
		return read
	}
	return unknown
}

// retrievalWords accepts a small POSIX quoting subset. Unclosed quotes, command
// substitutions, redirections and ambiguous syntax retain normal diagnostics.
func retrievalWords(s string) ([]string, bool) {
	var words []string
	var b strings.Builder
	var quote byte
	started := false
	flush := func() {
		if started {
			words = append(words, b.String())
			b.Reset()
			started = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && quote != '\'' {
			i++
			if i == len(s) {
				return nil, false
			}
			next := s[i]
			if next == '\n' {
				continue
			}
			if quote == '"' && !strings.ContainsRune("$`\"\\", rune(next)) {
				b.WriteByte('\\')
			}
			b.WriteByte(next)
			started = true
			continue
		}
		if quote != '\'' && (c == '$' || c == '`') {
			return nil, false
		}
		if c == quote && quote != 0 {
			quote = 0
			started = true
			continue
		}
		if quote == 0 {
			if strings.ContainsRune("<>(){}#", rune(c)) {
				return nil, false
			}
			if c == '\'' || c == '"' {
				quote = c
				started = true
				continue
			}
			if strings.ContainsRune(" \t\r\n\v\f", rune(c)) {
				flush()
				continue
			}
		}
		b.WriteByte(c)
		started = true
	}
	if quote != 0 {
		return nil, false
	}
	flush()
	return words, true
}

func shellRetrieval(command string, depth int) bool {
	if len(command) > 16*1024 || depth > 20 {
		return false
	}
	segments := shellSegments(command)
	if len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		words, ok := retrievalWords(segment)
		if !ok || !retrievalCommand(words, depth) {
			return false
		}
	}
	return true
}

func shortFlag(word, flags string) bool {
	return strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "--") && strings.ContainsAny(word, flags)
}

func skipOptions(args, values []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		option := args[0]
		args = args[1:]
		if option == "--" {
			break
		}
		if containsString(values, option) && len(args) > 0 {
			args = args[1:]
		}
	}
	return args
}

func assignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (i == 0 || !digit(c)) {
			return false
		}
	}
	return true
}

func retrievalCommand(words []string, depth int) bool {
	if depth > 20 {
		return false
	}
	for len(words) > 0 && assignment(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	program := words[0]
	args := words[1:]
	for _, dir := range []string{"/bin/", "/sbin/", "/usr/bin/", "/usr/sbin/", "/usr/local/bin/", "/opt/homebrew/bin/"} {
		if rest, ok := strings.CutPrefix(program, dir); ok {
			program = rest
			break
		}
	}
	var options []string
	wrapper := true
	switch program {
	case "sudo":
		options = []string{"-u", "-g", "-h", "-p", "-C", "-T", "--user", "--group", "--host"}
	case "env":
		options = []string{"-u", "--unset", "-C", "--chdir"}
	case "command", "nohup":
	case "exec":
		options = []string{"-a"}
	case "time":
		options = []string{"-f", "--format", "-o", "--output"}
	case "timeout":
		options = []string{"-s", "--signal", "-k", "--kill-after"}
	case "xargs":
		options = []string{"-n", "--max-args", "-P", "--max-procs", "-I", "--replace", "-d", "--delimiter", "-L", "--max-lines", "-s", "--max-chars"}
	default:
		wrapper = false
	}
	if wrapper {
		if program == "env" {
			for _, a := range args {
				if strings.HasPrefix(a, "--split-string") || shortFlag(a, "S") {
					return false
				}
			}
		}
		rest := skipOptions(args, options)
		if program == "timeout" && len(rest) > 0 {
			rest = rest[1:]
		}
		return (program == "env" && len(rest) == 0) || retrievalCommand(rest, depth+1)
	}
	if containsString([]string{"bash", "sh", "dash", "zsh", "ksh"}, program) {
		return len(args) > 1 && shortFlag(args[0], "c") && shellRetrieval(args[1], depth+1)
	}
	if program == "git" {
		return gitRetrieval(args)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "--version") {
		return true
	}
	has := func(s string) bool { return containsString(args, s) }
	switch program {
	case "find":
		for _, a := range args {
			if containsString([]string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf"}, a) {
				return false
			}
		}
		return true
	case "fd":
		for _, a := range args {
			if strings.HasPrefix(a, "--exec") || shortFlag(a, "xX") {
				return false
			}
		}
		return true
	case "sed":
		if len(args) < 2 || args[0] != "-n" {
			return false
		}
		r, ok := strings.CutSuffix(args[1], "p")
		if !ok || r == "" {
			return false
		}
		for _, c := range r {
			if (c < '0' || c > '9') && c != ',' && c != '$' {
				return false
			}
		}
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "-") {
				return false
			}
		}
		return true
	case "sort":
		for _, a := range args {
			if strings.HasPrefix(a, "--compress-program") || strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "-o") {
				return false
			}
		}
		return true
	case "rg":
		for _, a := range args {
			if strings.HasPrefix(a, "--pre") {
				return false
			}
		}
		return true
	case "xxd":
		return !has("-r") && !has("-revert")
	case "go":
		return len(args) > 0 && containsString([]string{"list", "doc", "env", "version"}, args[0]) && !has("-w") && !has("-u")
	case "docker", "podman":
		return len(args) > 0 && (args[0] == "ps" || args[0] == "version")
	}
	return containsString([]string{"cat", "grep", "ls", "nl", "head", "tail", "wc", "pwd", "stat", "file", "du", "df", "which", "type", "diff", "cmp", "jq", "uniq", "cut", "readlink", "realpath", "tree", "basename", "dirname", "printenv", "echo", "printf", "less", "more", "test", "[", "ps", "pgrep", "pkg-config", "strings", "uname", "od", "true", ":", "cd", "pstree", "sha256sum", "sha1sum", "md5sum", "lsof", "tr", "free", "id", "namei", "whoami", "paste", "ss", "getent"}, program)
}

func gitRetrieval(args []string) bool {
	args = skipOptions(args, []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace"})
	if len(args) == 0 {
		return false
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "diff":
		return !containsString(rest, "--check")
	case "branch":
		for _, a := range rest {
			if !containsString([]string{"-a", "-r", "--all", "--list", "--show-current", "-v", "-vv", "--verbose"}, a) {
				return false
			}
		}
		return true
	case "remote":
		return len(rest) == 0 || (len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose")) || rest[0] == "get-url"
	case "tag":
		return len(rest) == 0 || rest[0] == "-l" || rest[0] == "--list"
	case "config":
		return len(rest) > 0 && containsString([]string{"--get", "--get-all", "--list", "-l"}, rest[0])
	case "worktree":
		return len(rest) > 0 && rest[0] == "list"
	case "submodule":
		return len(rest) > 0 && rest[0] == "status"
	}
	return containsString([]string{"status", "log", "show", "blame", "ls-files", "ls-remote", "rev-parse", "merge-base", "grep", "describe", "show-ref", "check-ignore", "rev-list", "ls-tree", "diff-tree", "version"}, sub)
}

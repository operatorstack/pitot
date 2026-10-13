package main

import (
	"path"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type kimiHookStatus string

const (
	kimiHookConfigured kimiHookStatus = "FOUND"
	kimiHookMissing    kimiHookStatus = "MISSING"
	kimiHookInvalid    kimiHookStatus = "INVALID"
	kimiHookUnverified kimiHookStatus = "UNVERIFIED"
)

// inspectKimiHooks checks declarations only. It never executes a hook command,
// and recognizes a bounded command/matcher subset rather than guessing how the
// host interprets arbitrary shell scripts or regular expressions.
func inspectKimiHooks(data []byte, platform string) kimiHookStatus {
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		// Parser errors can contain source text, including provider credentials.
		return kimiHookInvalid
	}
	value, present := document["hooks"]
	if !present {
		return kimiHookMissing
	}
	hooks, ok := value.([]any)
	if !ok {
		return kimiHookInvalid
	}
	configured, unverified := false, false
	for _, value := range hooks {
		hook, ok := value.(map[string]any)
		if !ok || !validKimiHook(hook) {
			return kimiHookInvalid
		}
		if hook["event"] != "PreToolUse" {
			continue
		}
		matcherStatus := kimiBashMatcher(hook)
		if matcherStatus == kimiHookMissing {
			continue
		}
		commandStatus := kimiHookCommand(hook["command"].(string), platform)
		if commandStatus == kimiHookMissing {
			continue
		}
		if matcherStatus == kimiHookConfigured && commandStatus == kimiHookConfigured {
			configured = true
		} else {
			unverified = true
		}
	}
	if configured {
		return kimiHookConfigured
	}
	if unverified {
		return kimiHookUnverified
	}
	return kimiHookMissing
}

func validKimiHook(hook map[string]any) bool {
	for key := range hook {
		switch key {
		case "event", "matcher", "command", "timeout":
		default:
			return false
		}
	}
	event, ok := hook["event"].(string)
	if !ok {
		return false
	}
	// These event names are the published Kimi hook configuration contract.
	switch event {
	case "UserPromptSubmit", "UserPromptQueued", "PreToolUse", "Stop", "TurnStarted",
		"PostToolUse", "PostToolUseFailure", "PermissionRequest", "PermissionResult",
		"SessionStart", "SessionEnd", "SessionHeartbeat", "SubagentStart", "SubagentStop",
		"TaskStarted", "StopFailure", "Interrupt", "PreCompact", "PostCompact", "Notification":
	default:
		return false
	}
	if _, ok := hook["command"].(string); !ok {
		return false
	}
	if value, present := hook["matcher"]; present {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	if value, present := hook["timeout"]; present {
		timeout, ok := value.(int64)
		if !ok || timeout < 1 || timeout > 600 {
			return false
		}
	}
	return true
}

func kimiBashMatcher(hook map[string]any) kimiHookStatus {
	value, present := hook["matcher"]
	if !present {
		return kimiHookConfigured
	}
	switch value.(string) {
	case "Bash", "^Bash$", ".*":
		return kimiHookConfigured
	case "ReadFile":
		return kimiHookMissing
	default:
		return kimiHookUnverified
	}
}

func kimiHookCommand(command, platform string) kimiHookStatus {
	words, ok := kimiLiteralWords(command, platform)
	if !ok || len(words) == 0 {
		return kimiHookUnverified
	}
	executable := words[0]
	if executable != "pitot" && !(platform != "windows" && strings.Contains(executable, "/") && path.Base(executable) == "pitot") {
		if executable == "echo" || executable == "printf" {
			return kimiHookMissing
		}
		return kimiHookUnverified
	}
	if len(words) < 3 || words[1] != "hook" || words[2] != "kimi" {
		return kimiHookMissing
	}
	for i := 3; i < len(words); i += 2 {
		if words[i] != "--runtime" || i+1 >= len(words) || words[i+1] == "" {
			return kimiHookUnverified
		}
	}
	return kimiHookConfigured
}

// kimiLiteralWords accepts literal POSIX words with spaces/tabs and quotes.
// Expansions, escapes, operators and other shell syntax require manual review.
// Windows accepts the canonical bare command; quoting and executable-path
// interpretation remain unverified until the host's shell contract is known.
func kimiLiteralWords(command, platform string) ([]string, bool) {
	var words []string
	var word strings.Builder
	var quote byte
	started := false
	for i := 0; i < len(command); i++ {
		c := command[i]
		if platform == "windows" && !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || strings.ContainsRune("-_/.: \t", rune(c))) {
			return nil, false
		}
		if c == '\\' || c == '\n' || c == '\r' || c == 0 {
			return nil, false
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				if quote == '"' && (c == '$' || c == '`') {
					return nil, false
				}
				word.WriteByte(c)
			}
			continue
		}
		switch c {
		case ' ', '\t':
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		case '\'', '"':
			if platform == "windows" {
				return nil, false
			}
			quote, started = c, true
		case '$', '`', '|', '&', ';', '<', '>', '(', ')', '*', '?', '[', ']', '#', '~', '{', '}':
			return nil, false
		default:
			word.WriteByte(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		words = append(words, word.String())
	}
	return words, true
}

func currentKimiHookStatus(data []byte) kimiHookStatus {
	return inspectKimiHooks(data, runtime.GOOS)
}

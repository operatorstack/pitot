package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	qwen "github.com/operatorstack/pitot/integrations/qwen"
)

// Parse the JSON-with-comments syntax supported by Qwen without invoking its
// loader: the native loader can migrate and rewrite configuration files.
func doctorJSON(data []byte) (map[string]any, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	clean := append([]byte(nil), data...)
	quoted, escaped := false, false
	for i := 0; i < len(clean); i++ {
		if quoted {
			if escaped {
				escaped = false
			} else if clean[i] == '\\' {
				escaped = true
			} else if clean[i] == '"' {
				quoted = false
			}
			continue
		}
		if clean[i] == '"' {
			quoted = true
		} else if clean[i] == '/' && i+1 < len(clean) && clean[i+1] == '/' {
			for i < len(clean) && clean[i] != '\n' {
				clean[i] = ' '
				i++
			}
		} else if clean[i] == '/' && i+1 < len(clean) && clean[i+1] == '*' {
			clean[i], clean[i+1] = ' ', ' '
			i += 2
			for i+1 < len(clean) && !(clean[i] == '*' && clean[i+1] == '/') {
				if clean[i] != '\n' && clean[i] != '\r' {
					clean[i] = ' '
				}
				i++
			}
			if i+1 >= len(clean) {
				return nil, errors.New("unterminated comment")
			}
			clean[i], clean[i+1] = ' ', ' '
			i++
		}
	}
	var settings map[string]any
	if err := json.Unmarshal(clean, &settings); err != nil || settings == nil {
		return nil, errors.New("settings must be a JSON object")
	}
	return settings, nil
}

func doctorSetting(m map[string]any, keys ...string) any {
	var value any = m
	for _, key := range keys {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

func (r *readinessReport) qwenSettings(path string) map[string]any {
	data, err := readDoctorFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.result("settings", "INVALID", "a configuration file is unreadable or too large", true)
		return nil
	}
	settings, err := doctorJSON(data)
	if err != nil {
		r.result("settings", "INVALID", "a configuration file is not a valid JSON object with optional comments", true)
		return nil
	}
	r.result("settings", "READ", path, false)
	return settings
}

func qwenSystemPath(goos string) string {
	if path := os.Getenv("QWEN_CODE_SYSTEM_SETTINGS_PATH"); path != "" {
		return path
	}
	switch goos {
	case "windows":
		return `C:\ProgramData\qwen-code\settings.json`
	case "darwin":
		return "/Library/Application Support/QwenCode/settings.json"
	default:
		return "/etc/qwen-code/settings.json"
	}
}

func sameDoctorPath(a, b string) bool {
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	if real, err := filepath.EvalSymlinks(a); err == nil {
		a = real
	}
	if real, err := filepath.EvalSymlinks(b); err == nil {
		b = real
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func qwenTrustDecision(project string, rules map[string]any) (bool, bool) {
	project, _ = filepath.Abs(project)
	if real, err := filepath.EvalSymlinks(project); err == nil {
		project = real
	}
	depth, trusted := -1, false
	for path, value := range rules {
		level, ok := value.(string)
		if !ok || !filepath.IsAbs(path) || (level != "TRUST_FOLDER" && level != "TRUST_PARENT" && level != "DO_NOT_TRUST") {
			return false, false
		}
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		if level == "TRUST_PARENT" {
			path = filepath.Dir(path)
		}
		if runtime.GOOS == "windows" {
			path, project = strings.ToLower(path), strings.ToLower(project)
		}
		rel, err := filepath.Rel(path, project)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		n := len(strings.Split(filepath.Clean(path), string(filepath.Separator)))
		allow := level != "DO_NOT_TRUST"
		if n > depth || n == depth && !allow {
			depth, trusted = n, allow
		}
	}
	return trusted, true
}

// Deliberately recognize the documented simple matchers. JavaScript regular
// expressions have different semantics from Go's; do not silently substitute.
func qwenShellMatcher(value any) (match, known bool) {
	if value == nil {
		return true, true
	}
	pattern, ok := value.(string)
	if !ok {
		return false, false
	}
	pattern = strings.TrimSpace(pattern)
	switch pattern {
	case "", "*", ".*", "run_shell_command", "^run_shell_command$", "Bash":
		return true, true
	case "^Bash$", "read_file", "write_file", "^read_file$", "^write_file$":
		return false, true
	}
	if strings.Contains(pattern, "|") && !strings.HasPrefix(pattern, "^") && !strings.HasPrefix(pattern, "(") {
		known = true
		for _, part := range strings.Split(pattern, "|") {
			if strings.TrimSpace(part) == "" {
				continue
			}
			m, k := qwenShellMatcher(part)
			match, known = match || m, known && k
		}
		return match, known
	}
	return false, false
}

// Only tokenize literal executable arguments. Shell substitutions, pipelines
// and environment expansion need native inspection rather than execution here.
func doctorLiteralWords(command string) ([]string, bool) {
	if strings.ContainsAny(command, "$`;&|<>\n\r%!^") {
		return nil, false
	}
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	for _, c := range command {
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
		} else if c == '"' || c == '\'' {
			if c == '\'' && runtime.GOOS == "windows" {
				return nil, false
			}
			quote = c
			started = true
		} else if c == ' ' || c == '\t' {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		} else {
			if strings.ContainsRune("*?[]{}()#~", c) || c == '\\' && runtime.GOOS != "windows" {
				return nil, false
			}
			word.WriteRune(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		words = append(words, word.String())
	}
	return words, len(words) != 0
}

func (r *readinessReport) qwenCommand(command string, shell any) bool {
	words, ok := doctorLiteralWords(command)
	if !ok || len(words) > 3 {
		r.result("hook command", "UNVERIFIED", "use a literal path to the shipped Pitot bridge; dynamic commands are not executed by doctor", true)
		return false
	}
	if shell != nil && shell != "bash" && shell != "powershell" {
		r.result("hook shell", "UNVERIFIED", "unsupported shell selection", true)
		return false
	}
	shellBinary := "bash"
	if runtime.GOOS == "windows" {
		shellBinary = os.Getenv("ComSpec")
		if shellBinary == "" {
			shellBinary = "cmd.exe"
		}
		term := strings.ToLower(os.Getenv("TERM"))
		if os.Getenv("MSYSTEM") != "" || strings.Contains(strings.ToLower(os.Getenv("SHELL")), "bash") || strings.Contains(term, "msys") || strings.Contains(term, "cygwin") {
			r.result("hook shell", "UNVERIFIED", "inspect the effective MSYS/Git Bash shell in Qwen", true)
			return false
		}
	}
	if shell == "bash" {
		shellBinary = "bash"
	} else if shell == "powershell" {
		shellBinary = "powershell"
	}
	// These shells interpret executable quoting and backslashes differently.
	// Leave ambiguous forms for native inspection instead of guessing argv.
	selectedShell := strings.ToLower(filepath.Base(shellBinary))
	if strings.Contains(selectedShell, "powershell") || strings.Contains(selectedShell, "pwsh") {
		if strings.HasPrefix(strings.TrimSpace(command), "\"") || strings.HasPrefix(strings.TrimSpace(command), "'") {
			r.result("hook shell", "UNVERIFIED", "quoted PowerShell executable requires native invocation inspection", true)
			return false
		}
	}
	if selectedShell == "bash" && strings.Contains(command, `\`) {
		r.result("hook shell", "UNVERIFIED", "bash escape sequences require native inspection", true)
		return false
	}
	if r.binary(shellBinary) == "" {
		return false
	}
	bridge, expected, executable := words[0], qwen.BashBridge, true
	index := 1
	if len(words) >= 2 && (filepath.Base(words[0]) == "node" || filepath.Base(words[0]) == "node.exe" || filepath.Base(words[0]) == "bash") {
		if r.binary(words[0]) == "" {
			return false
		}
		bridge, executable, index = words[1], false, 2
		if filepath.Base(words[0]) != "bash" {
			expected = qwen.NodeBridge
		}
	}
	if len(words) > index+1 || !filepath.IsAbs(bridge) {
		r.result("hook command", "UNVERIFIED", "use an absolute bridge path and at most one literal Pitot executable argument", true)
		return false
	}
	data, err := readDoctorFile(bridge)
	if err != nil {
		r.result("hook bridge", "MISSING", "the configured bridge cannot be read", true)
		return false
	}
	if !bytes.Equal(data, []byte(expected)) {
		r.result("hook bridge", "UNVERIFIED", "script differs from the shipped Pitot bridge; review it independently", true)
		return false
	}
	if runtime.GOOS == "windows" && executable && shell != "bash" {
		r.result("hook shell", "UNVERIFIED", "use node with PreToolUse.cjs on Windows, or explicitly select bash", true)
		return false
	}
	if !r.file("hook bridge", bridge, executable) {
		return false
	}
	if expected == qwen.BashBridge {
		if r.binary("bash") == "" || r.binary("python3") == "" {
			return false
		}
	}
	pitot := os.Getenv("PITOT_BIN")
	if len(words) == index+1 {
		pitot = words[index]
	}
	if pitot == "" {
		pitot = "pitot"
	}
	return r.lookup("Pitot executable", pitot) != ""
}

func doctorQwen(stdout io.Writer) error {
	r := &readinessReport{out: stdout}
	r.binary("qwen")
	home, err := os.UserHomeDir()
	if err != nil {
		r.result("settings", "UNVERIFIED", "home directory cannot be resolved", true)
		return r.finish()
	}
	project, err := os.Getwd()
	if err != nil {
		return err
	}
	qwenHome := os.Getenv("QWEN_HOME")
	if qwenHome == "" {
		qwenHome = filepath.Join(home, ".qwen")
	} else if qwenHome == "~" {
		qwenHome = home
	} else if strings.HasPrefix(qwenHome, "~/") || strings.HasPrefix(qwenHome, `~\`) {
		qwenHome = filepath.Join(home, qwenHome[2:])
	}
	systemPath := qwenSystemPath(runtime.GOOS)
	defaultsPath := os.Getenv("QWEN_CODE_SYSTEM_DEFAULTS_PATH")
	if defaultsPath == "" {
		defaultsPath = filepath.Join(filepath.Dir(systemPath), "system-defaults.json")
	}
	defaults := r.qwenSettings(defaultsPath)
	user := r.qwenSettings(filepath.Join(qwenHome, "settings.json"))
	system := r.qwenSettings(systemPath)
	trustEnabled := false
	for _, layer := range []map[string]any{defaults, user, system} {
		if value := doctorSetting(layer, "security", "folderTrust", "enabled"); value != nil {
			if enabled, ok := value.(bool); ok {
				trustEnabled = enabled
			} else {
				r.result("folder trust", "UNVERIFIED", "unsupported setting type", true)
			}
		}
	}
	trusted := !trustEnabled
	if trustEnabled {
		trustPath := os.Getenv("QWEN_CODE_TRUSTED_FOLDERS_PATH")
		if trustPath == "" {
			trustPath = filepath.Join(qwenHome, "trustedFolders.json")
		}
		var known bool
		trusted, known = qwenTrustDecision(project, r.qwenSettings(trustPath))
		if !known {
			r.result("folder trust", "UNVERIFIED", "unsupported trust rule", true)
		}
	}
	var workspace map[string]any
	if trusted && !sameDoctorPath(project, home) {
		workspace = r.qwenSettings(filepath.Join(project, ".qwen", "settings.json"))
	} else {
		r.result("project settings", "EXCLUDED", "workspace is untrusted or equals the home directory", false)
	}
	layers := []map[string]any{defaults, user, workspace, system}
	disabled := false
	var sandbox any
	for _, layer := range []map[string]any{defaults, user, system} {
		if value := doctorSetting(layer, "tools", "executionSandbox"); value != nil {
			sandbox = value
		}
	}
	for _, layer := range layers {
		if value, exists := layer["disableAllHooks"]; exists {
			if flag, ok := value.(bool); ok {
				disabled = flag
			} else {
				r.result("hook activation", "UNVERIFIED", "unsupported disableAllHooks value", true)
			}
		}
	}
	if sandbox != nil && sandbox != false {
		r.result("execution sandbox", "UNVERIFIED", "configured sandbox can disable native hooks; verify the effective session mode", true)
	}
	if disabled {
		r.result("hook activation", "DISABLED", "effective disableAllHooks is true", true)
	}
	found := false
	var deferred bytes.Buffer
	hookReport := &readinessReport{out: &deferred}
	definitions := make(map[string][]byte)
	for _, layer := range layers {
		value := doctorSetting(layer, "hooks", "PreToolUse")
		if value == nil {
			continue
		}
		groups, ok := value.([]any)
		if !ok {
			r.result("PreToolUse", "INVALID", "hook groups must be an array", true)
			continue
		}
		for _, raw := range groups {
			group, ok := raw.(map[string]any)
			if !ok {
				r.result("PreToolUse", "INVALID", "hook group must be an object", true)
				continue
			}
			match, known := qwenShellMatcher(group["matcher"])
			if !known {
				hookReport.result("shell matcher", "UNVERIFIED", "inspect this JavaScript matcher in Qwen /hooks", true)
			}
			if known && !match {
				continue
			}
			hooks, ok := group["hooks"].([]any)
			if !ok {
				r.result("PreToolUse", "INVALID", "handlers must be an array", true)
				continue
			}
			for _, value := range hooks {
				hook, ok := value.(map[string]any)
				if !ok || hook["type"] != "command" {
					continue
				}
				// Native deduplication ignores options such as async and env.
				// Conflicting definitions cannot establish a blocking route here.
				command, ok := hook["command"].(string)
				if !ok {
					r.result("hook command", "INVALID", "expected a command string", true)
					continue
				}
				name, _ := hook["name"].(string)
				if value := hook["name"]; value != nil {
					if _, ok := value.(string); !ok {
						r.result("hook name", "INVALID", "expected a string", true)
						continue
					}
				}
				identity := command
				if name != "" {
					identity = name + ":" + command
				}
				// Native treats an absent, null or empty name identically.
				if name == "" {
					delete(hook, "name")
				}
				definition, _ := json.Marshal(hook)
				if previous, exists := definitions[identity]; exists && !bytes.Equal(previous, definition) {
					r.result("hook precedence", "UNVERIFIED", "duplicate hook identity has conflicting options; inspect the effective handler in Qwen", true)
				}
				definitions[identity] = definition
				if !known {
					continue
				}
				if value := hook["async"]; value != nil {
					if _, ok := value.(bool); !ok {
						r.result("shell hook", "INVALID", "async must be a boolean", true)
						continue
					}
				}
				if hook["async"] == true {
					r.result("shell hook", "ASYNCHRONOUS", "an asynchronous hook cannot block a shell action", false)
					continue
				}
				var detail bytes.Buffer
				candidate := &readinessReport{out: &detail}
				valid := candidate.qwenCommand(command, hook["shell"])
				if env, exists := hook["env"]; exists {
					values, ok := env.(map[string]any)
					if !ok || len(values) != 0 {
						candidate.result("hook environment", "UNVERIFIED", "per-hook overrides may change the command or runtime; inspect them in Qwen", true)
						valid = false
					}
				}
				if valid {
					_, _ = r.out.Write(detail.Bytes())
					found = true
				} else {
					_, _ = deferred.Write(detail.Bytes())
					hookReport.issues += candidate.issues
				}
			}
		}
	}
	if !found {
		_, _ = r.out.Write(deferred.Bytes())
		r.issues += hookReport.issues
		r.result("shell hook", "MISSING", "no inspected synchronous Pitot shell bridge; run pitot init --host qwen for setup", true)
	} else if deferred.Len() != 0 {
		r.result("additional hooks", "UNVERIFIED", "other hooks were not classified; the inspected Pitot route is present", false)
	}
	r.result("session activation", "UNVERIFIED", "inspect /hooks in Qwen; IDE trust, .env bootstrap, safe/bare modes, sandbox and session toggles can change activation", false)
	r.selectedRuntime()
	return r.finish()
}

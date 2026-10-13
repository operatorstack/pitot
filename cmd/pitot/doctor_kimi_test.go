package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKimiHookDeclarations(t *testing.T) {
	hook := func(matcher, command string) string {
		return fmt.Sprintf("[[hooks]]\nevent = 'PreToolUse'\nmatcher = %q\ncommand = %q\n", matcher, command)
	}
	for _, tc := range []struct {
		name, config string
		want         kimiHookStatus
	}{
		{"canonical", kimiHookConfig, kimiHookConfigured},
		{"literal TOML quotes", "[[hooks]]\nevent='PreToolUse'\nmatcher='Bash'\ncommand='pitot hook kimi'\n", kimiHookConfigured},
		{"inline array", "hooks=[{event='PreToolUse', matcher='Bash', command='pitot hook kimi'}]", kimiHookConfigured},
		{"comments", "# event = \"PreToolUse\"\n# command = \"pitot hook kimi\"", kimiHookMissing},
		{"unrelated values", "description='event = \"PreToolUse\"'\nnotes='pitot hook kimi'", kimiHookMissing},
		{"different entries", "[[hooks]]\nevent='PreToolUse'\ncommand='echo ordinary'\n[[hooks]]\nevent='PostToolUse'\ncommand='pitot hook kimi'", kimiHookMissing},
		{"different target", hook("ReadFile", "pitot hook kimi"), kimiHookMissing},
		{"echo", hook("Bash", "echo pitot hook kimi"), kimiHookMissing},
		{"wrong subcommand", hook("Bash", "pitot echo 'hook kimi'"), kimiHookMissing},
		{"wrong host", hook("Bash", "pitot hook claude"), kimiHookMissing},
		{"omitted matcher", "[[hooks]]\nevent='PreToolUse'\ncommand='pitot hook kimi'", kimiHookConfigured},
		{"wildcard", hook(".*", "pitot hook kimi"), kimiHookConfigured},
		{"anchored target", hook("^Bash$", "pitot hook kimi"), kimiHookConfigured},
		{"unknown regex", hook("Bash|ReadFile", "pitot hook kimi"), kimiHookUnverified},
		{"lookaround", hook("(?<=x)Bash", "pitot hook kimi"), kimiHookUnverified},
		{"empty matcher", hook("", "pitot hook kimi"), kimiHookUnverified},
		{"quoted words", hook("Bash", "'pitot' 'hook' 'kimi'"), kimiHookConfigured},
		{"quoted path", hook("Bash", "'/opt/Pitot installation/pitot' hook kimi"), kimiHookConfigured},
		{"runtime path", hook("Bash", "pitot hook kimi --runtime '/tmp/runtime path'"), kimiHookConfigured},
		{"whitespace", hook("Bash", "  pitot\t hook  kimi  "), kimiHookConfigured},
		{"dynamic command", hook("Bash", "$(which pitot) hook kimi"), kimiHookUnverified},
		{"runtime expansion", hook("Bash", "pitot hook kimi --runtime \"$RUNTIME\""), kimiHookUnverified},
		{"pipeline", hook("Bash", "pitot hook kimi | cat"), kimiHookUnverified},
		{"separate commands", hook("Bash", "pitot hook kimi\necho ordinary"), kimiHookUnverified},
		{"wrapper", hook("Bash", "sh -c 'pitot hook kimi'"), kimiHookUnverified},
		{"script", hook("Bash", "/opt/hooks/kimi.sh"), kimiHookUnverified},
		{"unsupported arguments", hook("Bash", "pitot hook kimi --other-option"), kimiHookUnverified},
		{"missing runtime value", hook("Bash", "pitot hook kimi --runtime"), kimiHookUnverified},
		{"missing command", "[[hooks]]\nevent='PreToolUse'", kimiHookInvalid},
		{"syntax", "[[hooks]]\nevent='PreToolUse'\ncommand='pitot hook kimi", kimiHookInvalid},
		{"hooks string", "hooks='pitot hook kimi'", kimiHookInvalid},
		{"non-table", "hooks=[1]", kimiHookInvalid},
		{"command type", "[[hooks]]\nevent='PreToolUse'\ncommand=1", kimiHookInvalid},
		{"matcher type", "[[hooks]]\nevent='PreToolUse'\ncommand='pitot hook kimi'\nmatcher=1", kimiHookInvalid},
		{"timeout range", hook("Bash", "pitot hook kimi") + "timeout=601", kimiHookInvalid},
		{"timeout type", hook("Bash", "pitot hook kimi") + "timeout=true", kimiHookInvalid},
		{"extra field", hook("Bash", "pitot hook kimi") + "extra='value'", kimiHookInvalid},
		{"unknown event", "[[hooks]]\nevent='Unknown'\ncommand='pitot hook kimi'", kimiHookInvalid},
		{"invalid peer", hook("Bash", "pitot hook kimi") + "[[hooks]]\nevent='PostToolUse'\ncommand=1", kimiHookInvalid},
		{"valid peer", hook("Bash", "pitot hook kimi") + "[[hooks]]\nevent='PostToolUse'\ncommand='echo ordinary'", kimiHookConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inspectKimiHooks([]byte(tc.config), "linux"); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestKimiCommandPlatformBoundaries(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		if got := kimiHookCommand("pitot hook kimi", platform); got != kimiHookConfigured {
			t.Fatalf("canonical command on %s: %s", platform, got)
		}
	}
	for _, command := range []string{"'pitot' hook kimi", `"C:\Program Files\Pitot\pitot.exe" hook kimi`, "pitot.exe hook kimi",
		"pitot hook kimi --runtime %RUNTIME%", "pitot hook kimi --runtime !RUNTIME!",
		"pitot hook kimi --runtime C:/foo^bar", "pitot hook kimi --runtime C:/foo%bar",
		"pitot hook kimi --runtime C:/foo!bar", "pitot hook kimi --runtime /tmp/runtime+path"} {
		if got := kimiHookCommand(command, "windows"); got != kimiHookUnverified {
			t.Fatalf("ambiguous Windows command %q: %s", command, got)
		}
	}
}

func TestDoctorKimiParserDiagnosticDoesNotExposeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	const secret = "test-provider-value-do-not-print"
	config := "provider_secret='" + secret + "'\n[[hooks]]\nevent='PreToolUse'\ncommand='unterminated"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep the external agent absent: this test concerns Pitot's parser output.
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := doctorHost("kimi", &stdout, &stderr); err == nil {
		t.Fatal("invalid configuration must fail the diagnostic")
	}
	output := stdout.String() + stderr.String()
	if !strings.Contains(output, "hook: INVALID") || strings.Contains(output, secret) || strings.Contains(output, "unterminated") {
		t.Fatalf("unexpected parser diagnostic: %s", output)
	}
}

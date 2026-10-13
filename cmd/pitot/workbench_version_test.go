package main

import (
	"strings"
	"testing"
)

func TestGeneratedSDKReleaseIdentity(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	for _, v := range []string{"1.2.3", "dev", ""} {
		version = v
		want := v
		if want == "dev" || want == "" {
			want = pitotPackageVersion
		}
		for lang, checks := range map[string]map[string]string{
			"python":     {"requirements.txt": "operatorstack-pitot==" + want, "pyproject.toml": "operatorstack-pitot==" + want},
			"typescript": {"package.json": `"@operatorstack/pitot": "` + want + `"`},
			"go":         {"go.mod": "require github.com/operatorstack/pitot v" + want},
			"rust":       {"Cargo.toml": `pitot = { git = "https://github.com/operatorstack/pitot", tag = "v` + want + `" }`},
		} {
			files, err := projectFiles(lang, "blank-consumer")
			if err != nil {
				t.Fatal(err)
			}
			for file, dependency := range checks {
				if !strings.Contains(files[file], dependency) {
					t.Errorf("version %q: %s missing %s", v, file, dependency)
				}
			}
		}
	}
}

package e2e_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/operatorstack/pitot/adapters"
)

func TestAllAdaptersHaveE2EScripts(t *testing.T) {
	testsDir := filepath.Join("..", "tests")

	for _, host := range adapters.Supported() {
		t.Run(string(host), func(t *testing.T) {
			scriptName := fmt.Sprintf("e2e_%s_cli_test.sh", host)
			scriptPath := filepath.Join(testsDir, scriptName)

			if info, err := os.Stat(scriptPath); err != nil || !info.Mode().IsRegular() {
				t.Errorf("Missing E2E test script for adapter %q: expected to find %s", host, scriptPath)
			}
		})
	}
}

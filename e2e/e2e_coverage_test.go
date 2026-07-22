package e2e_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/operatorstack/pitot/adapters"
)

func TestAllAdaptersHaveE2EScripts(t *testing.T) {
	// The e2e package lives in labs/15-pitot/pitot/e2e
	// The test scripts live in labs/15-pitot/tests
	testsDir := filepath.Join("..", "..", "tests")

	for _, host := range adapters.Supported() {
		t.Run(string(host), func(t *testing.T) {
			scriptName := fmt.Sprintf("e2e_%s_cli_test.sh", host)
			scriptPath := filepath.Join(testsDir, scriptName)

			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				t.Errorf("Missing E2E test script for adapter %q: expected to find %s", host, scriptPath)
			}
		})
	}
}
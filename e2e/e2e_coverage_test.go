package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/operatorstack/pitot/adapters"
)

// TestE2ECoverageSupervisoryControl implements the supervisory control gate
// ensuring that EVERY host adapter registered in the system is proven by an
// end-to-end real CLI integration test. The controller fails the build if
// any host lacks a live CLI verification script.
func TestE2ECoverageSupervisoryControl(t *testing.T) {
	hosts := adapters.Supported()

	for _, host := range hosts {
		t.Run(string(host), func(t *testing.T) {
			// Construct the expected integration script name
			scriptName := fmt.Sprintf("e2e_%s_cli_test.sh", host)
			// e2e tests run from within labs/15-pitot/pitot/e2e, so we walk up to the tests/ dir
			scriptPath := filepath.Join("..", "..", "tests", scriptName)

			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				t.Fatalf("Supervisory Control Failure: Missing end-to-end integration test for host %q. Expected script %q to exist to prove live CLI integration.", host, scriptPath)
			}
		})
	}
}

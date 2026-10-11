//go:build !windows

package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnershipRejectsInsecureOrLinkedLock(t *testing.T) {
	for _, mode := range []string{"insecure", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.json")
			lockPath := path + ".lock"
			if mode == "insecure" {
				if err := os.WriteFile(lockPath, nil, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(lockPath, 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, lockPath); err != nil {
					t.Fatal(err)
				}
			}
			if file, err := acquireRuntimeOwnership(path); err == nil {
				file.Close()
				t.Fatal("unsafe lock admitted")
			}
		})
	}
}

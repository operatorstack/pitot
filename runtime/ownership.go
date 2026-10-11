package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Keep the companion inode in place: unlinking a locked file lets a second
// starter create a different inode and become a concurrent owner.
func acquireRuntimeOwnership(path string) (*os.File, error) {
	if path == "" {
		return nil, errors.New("pitot: runtime descriptor path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("pitot: create runtime directory: %w", err)
	}
	file, err := lockRuntimeFile(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("pitot: acquire runtime ownership (path may belong to a live instance): %w", err)
	}
	return file, nil
}

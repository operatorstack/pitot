//go:build !windows

package runtime

import (
	"fmt"
	"os"
	"syscall"
)

func writeSecureDescriptorFile(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(contents); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return validateDescriptorSecurity(path)
}

func validateDescriptorSecurity(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("pitot: inspect runtime descriptor: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("pitot: runtime descriptor %q is accessible by another user", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("pitot: runtime descriptor %q is not owned by the current user", path)
	}
	return nil
}

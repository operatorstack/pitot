//go:build windows

package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOwnershipRejectsBroadWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	lockPath := path + ".lock"
	if err := writeSecureDescriptorFile(lockPath, nil); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(lockPath, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if file, err := acquireRuntimeOwnership(path); err == nil {
		file.Close()
		t.Fatal("broad lock ACL admitted")
	}
}

func TestOwnershipRejectsWindowsReparseLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	target := filepath.Join(t.TempDir(), "target")
	if err := writeSecureDescriptorFile(target, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Fatalf("create reparse fixture: %v", err)
	}
	if file, err := acquireRuntimeOwnership(path); err == nil {
		file.Close()
		t.Fatal("reparse lock admitted")
	}
}

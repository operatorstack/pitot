//go:build windows

package main

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func codexSystemDirectory() string {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		root = `C:\ProgramData`
	}
	return filepath.Join(root, "OpenAI", "Codex")
}

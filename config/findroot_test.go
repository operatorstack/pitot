package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// control-law: discovery-walks-up-to-owned-roots-only
//
// FindRoot resolves the nearest ancestor owning .pitot/conf.d, stops at the
// first .git boundary, and never descends into dependencies.
func TestFindRootWalksUpToOwnedRootsOnly(t *testing.T) {
	mk := func(t *testing.T, parts ...string) string {
		t.Helper()
		path := filepath.Join(parts...)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("subdirectory resolves the owning root", func(t *testing.T) {
		root := t.TempDir()
		mk(t, root, ".pitot", "conf.d")
		deep := mk(t, root, "apps", "web", "src")
		got, err := FindRoot(deep)
		if err != nil {
			t.Fatal(err)
		}
		if resolved, _ := filepath.EvalSymlinks(got); resolved != mustEval(t, root) {
			t.Fatalf("got %s, want %s", got, root)
		}
	})

	t.Run("git boundary stops the walk", func(t *testing.T) {
		outer := t.TempDir()
		mk(t, outer, ".pitot", "conf.d") // an owning root ABOVE the repo boundary
		repo := mk(t, outer, "some-repo")
		mk(t, repo, ".git")
		inside := mk(t, repo, "pkg")
		if _, err := FindRoot(inside); !errors.Is(err, ErrNoConfig) {
			t.Fatalf("the walk must stop at the repository's own .git boundary, got %v", err)
		}
	})

	t.Run("a dependency's substrate is invisible from outside it", func(t *testing.T) {
		repo := t.TempDir()
		mk(t, repo, ".git")
		mk(t, repo, "node_modules", "evil-pkg", ".pitot", "conf.d")
		if _, err := FindRoot(repo); !errors.Is(err, ErrNoConfig) {
			t.Fatalf("discovery must never descend into dependencies, got %v", err)
		}
	})

	t.Run("root with substrate and git resolves itself", func(t *testing.T) {
		repo := t.TempDir()
		mk(t, repo, ".git")
		mk(t, repo, ".pitot", "conf.d")
		got, err := FindRoot(repo)
		if err != nil {
			t.Fatal(err)
		}
		if mustEval(t, got) != mustEval(t, repo) {
			t.Fatalf("got %s, want %s", got, repo)
		}
	})
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicSessionStorage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	s := NewSessionStorage(path)
	if err := s.StoreSession(t.Context(), []byte("old")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("session file: %v, %v", info, err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if probe, err := os.CreateTemp(dir, ".probe-*"); err == nil {
		// Privileged runs (e.g. root in containers) ignore directory modes, so the write cannot be made to fail.
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Log("directory permissions not enforced; skipping write-failure case")
	} else {
		if err := s.StoreSession(t.Context(), []byte("new")); err == nil {
			t.Fatal("store into read-only directory succeeded")
		}
		if data, err := s.LoadSession(t.Context()); err != nil || string(data) != "old" {
			t.Fatalf("previous session lost: %q, %v", data, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Fatalf("temporary files left: %v", entries)
		}
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreSession(t.Context(), []byte("new")); err != nil {
		t.Fatal(err)
	}
	if data, err := s.LoadSession(t.Context()); err != nil || string(data) != "new" {
		t.Fatalf("session not replaced: %q, %v", data, err)
	}
}

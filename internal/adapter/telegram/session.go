package telegram

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/session"
)

// NewSessionStorage stores the session at path, replacing the file via rename, so a crash
// or full disk mid-write keeps the previous session instead of a truncated one.
func NewSessionStorage(path string) session.Storage {
	return &atomicSessionStorage{FileStorage: session.FileStorage{Path: path}}
}

type atomicSessionStorage struct {
	session.FileStorage
	mu sync.Mutex
}

func (s *atomicSessionStorage) StoreSession(_ context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.CreateTemp(filepath.Dir(s.Path), "."+filepath.Base(s.Path)+"-*") // created 0600
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}

	defer func() { _ = os.Remove(f.Name()) }() // no-op after a successful rename

	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return fmt.Errorf("write session file: %w", err)
	}

	if err := os.Rename(f.Name(), s.Path); err != nil {
		return fmt.Errorf("replace session file: %w", err)
	}

	return nil
}

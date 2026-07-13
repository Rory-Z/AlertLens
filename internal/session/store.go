package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot Snapshot
	readyErr error
}

type updateError struct {
	err       error
	committed bool
}

func (e *updateError) Error() string { return e.err.Error() }
func (e *updateError) Unwrap() error { return e.err }

func UpdateCommitted(err error) bool {
	var updateErr *updateError
	return errors.As(err, &updateErr) && updateErr.committed
}

func Open(path string, now func() time.Time) (*Store, error) {
	if path == "" {
		return nil, errors.New("state path is empty")
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	snapshot := Snapshot{
		Version:  CurrentVersion,
		Sessions: make(map[string]Record),
		EventIDs: make(map[string]time.Time),
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, fmt.Errorf("decode state: %w", err)
		}
		if snapshot.Version != CurrentVersion {
			return nil, fmt.Errorf("unsupported state version %d", snapshot.Version)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if snapshot.Sessions == nil {
		snapshot.Sessions = make(map[string]Record)
	}
	if snapshot.EventIDs == nil {
		snapshot.EventIDs = make(map[string]time.Time)
	}
	prune(&snapshot, now())

	store := &Store{path: path, snapshot: snapshot}
	if _, err := store.persist(snapshot); err != nil {
		return nil, fmt.Errorf("persist state: %w", err)
	}
	return store, nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.snapshot)
}

func (s *Store) Update(update func(*Snapshot) error) error {
	return s.update(update, false)
}

func (s *Store) UpdateRetainingOnPersistenceFailure(update func(*Snapshot) error) error {
	return s.update(update, true)
}

func (s *Store) update(update func(*Snapshot) error, retainOnPersistenceFailure bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := clone(s.snapshot)
	if err := update(&next); err != nil {
		return err
	}
	committed, err := s.persist(next)
	if committed || retainOnPersistenceFailure {
		s.snapshot = next
	}
	if err != nil {
		err = &updateError{err: err, committed: committed}
		s.readyErr = err
		return err
	}
	s.snapshot = next
	s.readyErr = nil
	return nil
}

func (s *Store) Ready() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readyErr
}

func (s *Store) Prune(now time.Time) error {
	return s.Update(func(snapshot *Snapshot) error {
		prune(snapshot, now)
		return nil
	})
}

func (s *Store) persist(snapshot Snapshot) (bool, error) {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".alertlens-state-*")
	if err != nil {
		return false, fmt.Errorf("create temporary state: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	if err := tmp.Chmod(0o600); err != nil {
		return false, fmt.Errorf("set state permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return false, fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return false, fmt.Errorf("sync state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return false, fmt.Errorf("replace state: %w", err)
	}

	directory, err := os.Open(dir)
	if err != nil {
		return true, fmt.Errorf("open state directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return true, fmt.Errorf("sync state directory: %w", err)
	}
	return true, nil
}

func prune(snapshot *Snapshot, now time.Time) {
	for key, record := range snapshot.Sessions {
		if !record.ExpiresAt.IsZero() && !record.ExpiresAt.After(now) {
			delete(snapshot.Sessions, key)
		}
	}
	for id, expiresAt := range snapshot.EventIDs {
		if !expiresAt.After(now) {
			delete(snapshot.EventIDs, id)
		}
	}
}

func clone(snapshot Snapshot) Snapshot {
	copy := Snapshot{
		Version:  snapshot.Version,
		Sessions: make(map[string]Record, len(snapshot.Sessions)),
		EventIDs: make(map[string]time.Time, len(snapshot.EventIDs)),
	}
	for key, record := range snapshot.Sessions {
		record.AlertContext = append(json.RawMessage(nil), record.AlertContext...)
		record.Conversation = append([]ConversationTurn(nil), record.Conversation...)
		copy.Sessions[key] = record
	}
	for key, expiresAt := range snapshot.EventIDs {
		copy.EventIDs[key] = expiresAt
	}
	return copy
}

package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(snapshot *Snapshot) error {
		snapshot.Sessions["am:HighCPU:prod"] = Record{
			Key:       "am:HighCPU:prod",
			Type:      "alert",
			State:     "active",
			Channel:   "C1",
			ParentTS:  "1.2",
			UpdatedAt: testNow,
			Conversation: []ConversationTurn{
				{Role: "user", Content: "why?"},
			},
		}
		snapshot.EventIDs["Ev1"] = testNow.Add(time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Snapshot()
	if got.Version != CurrentVersion || got.Sessions["am:HighCPU:prod"].ParentTS != "1.2" ||
		got.Sessions["am:HighCPU:prod"].Conversation[0].Content != "why?" ||
		!got.EventIDs["Ev1"].Equal(testNow.Add(time.Minute)) {
		t.Fatalf("snapshot = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestStoreSnapshotIsACopy(t *testing.T) {
	store := openTestStore(t)
	if err := store.Update(func(snapshot *Snapshot) error {
		snapshot.Sessions["am:A:ns"] = Record{
			Key: "am:A:ns", AlertContext: []byte(`{"labels":{"alertname":"A"}}`),
			Conversation: []ConversationTurn{{Role: "assistant", Content: "answer"}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	copy := store.Snapshot()
	record := copy.Sessions["am:A:ns"]
	record.AlertContext[0] = 'x'
	record.Conversation[0].Content = "changed"
	copy.Sessions["am:A:ns"] = record
	delete(copy.Sessions, "am:A:ns")

	got := store.Snapshot().Sessions["am:A:ns"]
	if string(got.AlertContext) != `{"labels":{"alertname":"A"}}` || got.Conversation[0].Content != "answer" {
		t.Fatalf("internal snapshot was mutated: %#v", got)
	}
}

func TestStoreRejectsInvalidSnapshots(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
	}{
		{name: "corrupt JSON", data: "{"},
		{name: "unsupported version", data: `{"version":2,"sessions":{},"eventIds":{}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path, func() time.Time { return testNow }); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestOpenRejectsInvalidPaths(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if _, err := Open("", nil); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("parent is a file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(filepath.Join(parent, "state.json"), nil); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("state path is a directory", func(t *testing.T) {
		if _, err := Open(t.TempDir(), nil); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestOpenInitializesNullMapsWithDefaultClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"sessions":null,"eventIds":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot()
	if got.Sessions == nil || got.EventIDs == nil {
		t.Fatalf("maps were not initialized: %#v", got)
	}
}

func TestOpenPrunesExpiredState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data := `{
  "version": 1,
  "sessions": {
    "expired": {"key":"expired","expiresAt":"2026-07-10T00:00:00Z"},
    "active": {"key":"active","expiresAt":"2026-07-12T00:00:00Z"}
  },
  "eventIds": {
    "expired":"2026-07-10T00:00:00Z",
    "active":"2026-07-12T00:00:00Z"
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot()
	if len(got.Sessions) != 1 || got.Sessions["active"].Key != "active" ||
		len(got.EventIDs) != 1 || got.EventIDs["active"].IsZero() {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestUpdateCallbackErrorLeavesStateUnchanged(t *testing.T) {
	store := openTestStore(t)
	wantErr := errors.New("reject update")
	err := store.Update(func(snapshot *Snapshot) error {
		snapshot.Sessions["new"] = Record{Key: "new"}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
	if len(store.Snapshot().Sessions) != 0 {
		t.Fatalf("snapshot changed: %#v", store.Snapshot())
	}
	if err := store.Ready(); err != nil {
		t.Fatalf("callback error degraded readiness: %v", err)
	}
}

func TestUpdateEncodeFailureDegradesReadiness(t *testing.T) {
	store := openTestStore(t)
	err := store.Update(func(snapshot *Snapshot) error {
		snapshot.Sessions["bad"] = Record{Key: "bad", AlertContext: []byte("{")}
		return nil
	})
	if err == nil || store.Ready() == nil {
		t.Fatalf("update error = %v, ready error = %v", err, store.Ready())
	}
	if len(store.Snapshot().Sessions) != 0 {
		t.Fatalf("failed update changed memory: %#v", store.Snapshot())
	}
}

func TestWriteFailureDegradesReadinessAndPreservesState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "state.json"), func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = store.Update(func(snapshot *Snapshot) error {
		snapshot.Sessions["lost"] = Record{Key: "lost"}
		return nil
	})
	if err == nil || store.Ready() == nil {
		t.Fatalf("write error = %v, ready error = %v", err, store.Ready())
	}
	if len(store.Snapshot().Sessions) != 0 {
		t.Fatalf("failed update changed memory: %#v", store.Snapshot())
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(snapshot *Snapshot) error {
		snapshot.Sessions["saved"] = Record{Key: "saved"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(); err != nil {
		t.Fatalf("readiness did not recover: %v", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "state.json"), func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	return store
}

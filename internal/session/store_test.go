package session

import (
	"io"
	"os"
	"testing"
	"time"
)

func TestStoreReturnsIsolatedStatesBySessionID(t *testing.T) {
	store := NewStore(time.Hour)
	now := time.Unix(100, 0)

	a := store.Get("a", now)
	b := store.Get("b", now)
	a.State.Mark("a-thought")
	b.State.Mark("b-thought")

	if got := store.Get("a", now).State.Marks(); got[0] != "a-thought" {
		t.Fatalf("session a marks = %#v", got)
	}
	if got := store.Get("b", now).State.Marks(); got[0] != "b-thought" {
		t.Fatalf("session b marks = %#v", got)
	}
}

func TestStoreCleanupExpiresIdleSessions(t *testing.T) {
	store := NewStore(10 * time.Second)
	store.Get("stale", time.Unix(100, 0))
	store.Get("fresh", time.Unix(111, 0))

	removed := store.Cleanup(time.Unix(120, 0))

	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if store.Exists("stale") {
		t.Fatal("stale session still exists")
	}
	if !store.Exists("fresh") {
		t.Fatal("fresh session was removed")
	}
}

func TestStoreResolveHandleMintsAndRequiresKnownHandles(t *testing.T) {
	store := NewStore(time.Hour)
	now := time.Unix(100, 0)

	created, minted, err := store.ResolveHandle("", now)
	if err != nil || !minted || created.ID == "" {
		t.Fatalf("ResolveHandle create = (%#v, %t, %v)", created, minted, err)
	}
	resolved, minted, err := store.ResolveHandle(created.ID, now.Add(time.Second))
	if err != nil || minted || resolved != created {
		t.Fatalf("ResolveHandle existing = (%#v, %t, %v)", resolved, minted, err)
	}
	if _, _, err := store.ResolveHandle("not-server-minted", now); err == nil {
		t.Fatal("ResolveHandle accepted an unknown handle")
	}
}

func TestStoreUsesEnvDefaultForThoughtLogging(t *testing.T) {
	t.Setenv("DISABLE_THOUGHT_LOGGING", "")
	store := NewStore(time.Hour)
	session := store.Get("logging", time.Unix(100, 0))

	output := captureStderr(t, func() {
		_, err := session.State.Process(map[string]any{
			"thought":           "store default log",
			"thoughtNumber":     1,
			"totalThoughts":     1,
			"nextThoughtNeeded": false,
		})
		if err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
	})

	if output == "" {
		t.Fatal("stderr output is empty, want default logging enabled")
	}
}

func TestStoreHonorsDisableThoughtLoggingEnv(t *testing.T) {
	t.Setenv("DISABLE_THOUGHT_LOGGING", "true")
	store := NewStore(time.Hour)
	session := store.Get("quiet", time.Unix(100, 0))

	output := captureStderr(t, func() {
		_, err := session.State.Process(map[string]any{
			"thought":           "store hidden log",
			"thoughtNumber":     1,
			"totalThoughts":     1,
			"nextThoughtNeeded": false,
		})
		if err != nil {
			t.Fatalf("Process returned error: %v", err)
		}
	})

	if output != "" {
		t.Fatalf("stderr output = %q, want empty output", output)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = old
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(data)
}

package state

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"aiquota/internal/model"
)

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUpdateKeepsNewerObservation(t *testing.T) {
	s := Store{Dir: privateTestDir(t)}
	now := time.Now().UTC()
	p := model.Provider{ID: "claude", Name: "Claude", ObservedAt: &now}
	if err := s.Update("claude", func(_ *model.Provider) (model.Provider, error) { return p, nil }); err != nil {
		t.Fatal(err)
	}
	older := now.Add(-time.Hour)
	if err := s.Update("claude", func(old *model.Provider) (model.Provider, error) {
		if old == nil {
			t.Fatal("missing old")
		}
		if old.ObservedAt.After(older) {
			return *old, nil
		}
		p.ObservedAt = &older
		return p, nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("claude")
	if err != nil || !got.ObservedAt.Equal(now) {
		t.Fatalf("got %+v, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(s.Dir, "claude.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	s := Store{Dir: privateTestDir(t)}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Update("claude", func(old *model.Provider) (model.Provider, error) {
				if old == nil {
					return model.Provider{ID: "claude", Name: "x"}, nil
				}
				old.Name += "x"
				return *old, nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	p, err := s.Read("claude")
	if err != nil || len(p.Name) != 20 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestInvalidIDAndCorruptState(t *testing.T) {
	s := Store{Dir: privateTestDir(t)}
	if _, err := s.Read("../secret"); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "claude.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("claude"); err == nil {
		t.Fatal("accepted corrupt state")
	}
}

func TestDoesNotFollowStateSymlink(t *testing.T) {
	dir := privateTestDir(t)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "claude.json")); err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: dir}
	if _, err := s.Read("claude"); err == nil {
		t.Fatal("followed symlink")
	}
	if err := s.Update("claude", func(*model.Provider) (model.Provider, error) { return model.Provider{ID: "claude"}, nil }); err == nil {
		t.Fatal("overwrote symlinked state")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "secret" {
		t.Fatalf("target changed: %q, %v", got, err)
	}
}

func TestUpdateRecoversCorruptState(t *testing.T) {
	s := Store{Dir: privateTestDir(t)}
	if err := os.WriteFile(filepath.Join(s.Dir, "codex.json"), []byte(`{"schema_version":`), 0600); err != nil {
		t.Fatal(err)
	}
	err := s.Update("codex", func(old *model.Provider) (model.Provider, error) {
		if old != nil {
			t.Fatal("corrupt state was passed as an observation")
		}
		return model.Provider{ID: "codex", Name: "recovered"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Read("codex")
	if err != nil || p.Name != "recovered" {
		t.Fatalf("got %+v: %v", p, err)
	}
}

func TestUpdatePreservesUnsupportedSchema(t *testing.T) {
	s := Store{Dir: privateTestDir(t)}
	path := filepath.Join(s.Dir, "codex.json")
	future := []byte(`{"schema_version":999,"provider":{"id":"codex"}}`)
	if err := os.WriteFile(path, future, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("codex", func(*model.Provider) (model.Provider, error) { return model.Provider{ID: "codex"}, nil }); err == nil {
		t.Fatal("accepted future schema")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(future) {
		t.Fatalf("future data changed: %s %v", got, err)
	}
}

func TestReadRejectsSpecialFile(t *testing.T) {
	s := Store{Dir: privateTestDir(t)}
	if err := syscall.Mkfifo(filepath.Join(s.Dir, "claude.json"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Read("claude"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted FIFO")
		}
	case <-time.After(time.Second):
		t.Fatal("read blocked on FIFO")
	}
}

func TestReadRejectsPublicStateFile(t *testing.T) {
	s := Store{Dir: privateTestDir(t)}
	path := filepath.Join(s.Dir, "claude.json")
	data := []byte(`{"schema_version":1,"provider":{"id":"claude"}}`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("claude"); err == nil {
		t.Fatal("accepted publicly readable state file")
	}
}

func TestReadRejectsPublicStateDirectory(t *testing.T) {
	s := Store{Dir: privateTestDir(t)}
	path := filepath.Join(s.Dir, "claude.json")
	data := []byte(`{"schema_version":1,"provider":{"id":"claude"}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("claude"); err == nil {
		t.Fatal("accepted publicly accessible state directory")
	}
}

func TestUpdateRejectsInsecureDirectoryAndLock(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		dir := privateTestDir(t)
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		s := Store{Dir: dir}
		if err := s.Update("claude", func(*model.Provider) (model.Provider, error) { return model.Provider{ID: "claude"}, nil }); err == nil {
			t.Fatal("accepted public state directory")
		}
	})
	t.Run("lock", func(t *testing.T) {
		dir := privateTestDir(t)
		path := filepath.Join(dir, "claude.lock")
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		s := Store{Dir: dir}
		if err := s.Update("claude", func(*model.Provider) (model.Provider, error) { return model.Provider{ID: "claude"}, nil }); err == nil {
			t.Fatal("accepted public lock file")
		}
	})
}

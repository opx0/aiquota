package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"aiquota/internal/model"
)

type Store struct{ Dir string }

type record struct {
	SchemaVersion int            `json:"schema_version"`
	Provider      model.Provider `json:"provider"`
}

var errCorrupt = errors.New("corrupt state")

var validID = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func (s Store) path(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", errors.New("invalid provider ID")
	}
	if s.Dir == "" {
		return "", errors.New("empty state directory")
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

func (s Store) Read(id string) (model.Provider, error) {
	var zero model.Provider
	path, err := s.path(id)
	if err != nil {
		return zero, err
	}
	dirInfo, err := os.Lstat(s.Dir)
	if err != nil {
		return zero, fmt.Errorf("stat state directory: %w", err)
	}
	dirOwner, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !dirInfo.IsDir() || dirInfo.Mode().Perm()&0077 != 0 || !ok || int(dirOwner.Uid) != os.Geteuid() {
		return zero, errors.New("state directory must be owned by the current user and private (mode 0700)")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return zero, fmt.Errorf("open state: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return zero, fmt.Errorf("stat state: %w", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Geteuid() {
		return zero, errors.New("state file must be a private regular file owned by the current user")
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return zero, fmt.Errorf("read state: %w", err)
	}
	if len(b) > 1<<20 {
		return zero, fmt.Errorf("%w: file exceeds 1 MiB", errCorrupt)
	}
	var v record
	if err := json.Unmarshal(b, &v); err != nil {
		return zero, fmt.Errorf("%w: decode JSON: %v", errCorrupt, err)
	}
	if v.SchemaVersion != model.SchemaVersion || v.Provider.ID != id {
		return zero, errors.New("invalid state schema or provider ID")
	}
	return v.Provider, nil
}

func (s Store) Update(id string, fn func(*model.Provider) (model.Provider, error)) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Geteuid() {
		return errors.New("state directory must be owned by the current user and private (mode 0700)")
	}
	lockPath := filepath.Join(s.Dir, id+".lock")
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer lock.Close()
	lockInfo, err := lock.Stat()
	if err != nil {
		return fmt.Errorf("stat state lock: %w", err)
	}
	lockOwner, ok := lockInfo.Sys().(*syscall.Stat_t)
	if !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0077 != 0 || !ok || int(lockOwner.Uid) != os.Geteuid() {
		return errors.New("state lock must be a private regular file owned by the current user")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	old, err := s.Read(id)
	var previous *model.Provider
	if err == nil {
		previous = &old
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errCorrupt) {
		return err
	}
	next, err := fn(previous)
	if err != nil {
		return err
	}
	if next.ID != id {
		return errors.New("provider ID does not match state file")
	}
	data, err := json.Marshal(record{SchemaVersion: model.SchemaVersion, Provider: next})
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	tmp, err := os.CreateTemp(s.Dir, ".aiquota-*")
	if err != nil {
		return fmt.Errorf("create state temporary file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}

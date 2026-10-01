// Package store keeps what was declared -- every spec and whether it should run -- on
// disk, one file per container, so the supervisor brings containers back after its own
// restart or a reboot from what was persisted, never from memory alone.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
)

// The desired run states.
const (
	Running = "running"
	Stopped = "stopped"
)

// Record is one container's declaration.
type Record struct {
	Spec    spec.Spec `json:"spec"`
	Desired string    `json:"desired"`
}

// Store is a directory of records.
type Store struct{ dir string }

const (
	suffix = ".json"
	bootID = "boot-id"
)

// Open makes dir (mode 0700) when it is missing.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir is where the records are.
func (s *Store) Dir() string { return s.dir }

// Load reads every record. A file that does not parse, fails validation or does not
// carry its own name is reported in bad and left on disk for a person; the others load.
func (s *Store) Load() (records map[string]Record, bad []error, err error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, err
	}
	records = map[string]Record{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), suffix)
		if !ok || e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, nil, err
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if r.Spec.Name != name {
			bad = append(bad, fmt.Errorf("%s: names container %q", e.Name(), r.Spec.Name))
			continue
		}
		if err := r.Spec.Validate(); err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if r.Desired != Running && r.Desired != Stopped {
			bad = append(bad, fmt.Errorf("%s: desired state %q", e.Name(), r.Desired))
			continue
		}
		records[name] = r
	}
	return records, bad, nil
}

// Put writes one record atomically: a crash leaves the old file or the new one.
func (s *Store) Put(r Record) error {
	if !spec.ValidName(r.Spec.Name) {
		return fmt.Errorf("%q is not a container name", r.Spec.Name)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return s.write(r.Spec.Name+suffix, append(b, '\n'))
}

// Delete removes one record; a missing one is not an error.
func (s *Store) Delete(name string) error {
	if !spec.ValidName(name) {
		return fmt.Errorf("%q is not a container name", name)
	}
	err := os.Remove(filepath.Join(s.dir, name+suffix))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDir(s.dir)
}

// BootID is the boot the records were last brought up in, "" before the first.
func (s *Store) BootID() (string, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, bootID))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// SetBootID records the boot the records are brought up in.
func (s *Store) SetBootID(id string) error {
	return s.write(bootID, []byte(id+"\n"))
}

func (s *Store) write(name string, b []byte) error {
	f, err := os.CreateTemp(s.dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) //nolint:errcheck // gone after the rename; removed on any failure before it
	if _, err := f.Write(b); err != nil {
		f.Close() //nolint:errcheck,gosec // the write already failed
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close() //nolint:errcheck,gosec // the sync already failed
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, name)); err != nil {
		return err
	}
	return syncDir(s.dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck // read-only handle
	return d.Sync()
}

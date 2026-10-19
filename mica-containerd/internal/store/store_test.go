package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
)

func rec(name, desired string) Record {
	return Record{Spec: spec.Spec{Name: name, Project: "p", Service: "s", Network: "p_default", Image: "docker.io/library/busybox:1"}, Desired: desired}
}

func TestPutLoadDelete(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "containerd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []Record{rec("a", Running), rec("b", Stopped)} {
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	got, bad, err := s.Load()
	if err != nil || len(bad) != 0 {
		t.Fatalf("load: %v %v", err, bad)
	}
	if len(got) != 2 || got["a"].Desired != Running || got["b"].Desired != Stopped {
		t.Fatalf("loaded %+v", got)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("deleting a missing record: %v", err)
	}
	got, _, _ = s.Load()
	if _, ok := got["a"]; ok || len(got) != 1 {
		t.Fatalf("after delete: %+v", got)
	}
}

func TestPutReplacesAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Put(rec("a", Running))
	_ = s.Put(rec("a", Stopped))
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 || entries[0].Name() != "a.json" || entries[1].Name() != "projects" {
		t.Fatalf("directory holds %v", entries)
	}
	got, _, _ := s.Load()
	if got["a"].Desired != Stopped {
		t.Fatal("the second put did not replace the first")
	}
}

func TestLoadReportsBadFilesAndLoadsTheRest(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Put(rec("good", Running))
	files := map[string]string{
		"torn.json":    `{"spec": {"name": "torn"`,
		"renamed.json": `{"spec": {"name": "other", "image": "i"}, "desired": "running"}`,
		"invalid.json": `{"spec": {"name": "invalid", "project": "p", "service": "s", "network": "p_default", "image": ""}, "desired": "running"}`,
		"state.json":   `{"spec": {"name": "state", "project": "p", "service": "s", "network": "p_default", "image": "i"}, "desired": "paused"}`,
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, bad, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(bad) != len(files) {
		t.Fatalf("loaded %d, bad %v", len(got), bad)
	}
}

func TestPutRefusesANameThatIsAPath(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Put(rec("../escape", Running)); err == nil {
		t.Fatal("a name with a path was written")
	}
	if err := s.Delete("../escape"); err == nil {
		t.Fatal("a name with a path was deleted")
	}
}

func TestBootID(t *testing.T) {
	s, _ := Open(t.TempDir())
	if id, err := s.BootID(); err != nil || id != "" {
		t.Fatalf("before the first boot: %q %v", id, err)
	}
	_ = s.SetBootID("b1")
	if id, _ := s.BootID(); id != "b1" {
		t.Fatalf("boot id %q", id)
	}
	if _, _, err := s.Load(); err != nil {
		t.Fatal("the boot id file broke Load")
	}
}

func TestProjectsApartFromTheRecords(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Put(rec("web-app-1", Running))
	if err := s.PutProject("web", Project{Compose: []byte("services: {}\n"), Dir: "/srv/web"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Projects()
	if err != nil || len(got) != 1 || string(got["web"].Compose) != "services: {}\n" || got["web"].Dir != "/srv/web" {
		t.Fatalf("projects %+v %v", got, err)
	}
	if records, _, _ := s.Load(); len(records) != 1 {
		t.Fatalf("a project was loaded as a record: %+v", records)
	}
	// Declared again without a directory, it has none.
	if err := s.PutProject("web", Project{Compose: []byte("services: {}\n")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Projects(); got["web"].Dir != "" {
		t.Fatalf("the old directory stayed: %+v", got)
	}
	if err := s.PutProject("../escape", Project{}); err == nil {
		t.Fatal("a project name with a path was written")
	}
	if err := s.DeleteProject("web"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject("web"); err != nil {
		t.Fatalf("deleting a missing project: %v", err)
	}
	if got, _ := s.Projects(); len(got) != 0 {
		t.Fatalf("after delete: %+v", got)
	}
}

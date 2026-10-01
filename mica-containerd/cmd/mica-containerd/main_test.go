package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/api"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/supervisor"
)

func TestReadBootTime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stat")
	_ = os.WriteFile(p, []byte("cpu  1 2 3\nbtime 1790000000\nprocesses 9\n"), 0o600)
	if got, err := readBootTime(p); err != nil || got.Unix() != 1790000000 {
		t.Fatalf("%v %v", got, err)
	}
	_ = os.WriteFile(p, []byte("cpu 1\n"), 0o600)
	if _, err := readBootTime(p); err == nil {
		t.Fatal("a stat without btime was read")
	}
}

func TestCtlUsageAndRefusals(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := ctl(nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Fatalf("no command: %d %q", code, errOut.String())
	}
	if code := ctl([]string{"get"}, &out, &errOut); code != 2 {
		t.Fatalf("get without a name: %d", code)
	}
	if code := ctl([]string{"--socket", "/nonexistent/api.sock", "list"}, &out, &errOut); code != 1 {
		t.Fatalf("no daemon: %d", code)
	}
}

func TestCtlAgainstASocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "mcd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "api.sock")
	l, err := api.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	sv := &fakeSV{specs: map[string]spec.Spec{}}
	srv := &http.Server{Handler: api.Handler(sv, api.Info{Version: "t"}), ReadHeaderTimeout: time.Second}
	go srv.Serve(l) //nolint:errcheck // closed below
	defer srv.Close()

	specFile := filepath.Join(dir, "web.json")
	_ = os.WriteFile(specFile, []byte(`{"image": "docker.io/library/nginx:1"}`), 0o600)
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := ctl(append([]string{"--socket", sock}, args...), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, _, e := run("put", "web", specFile); code != 0 {
		t.Fatalf("put: %d %s", code, e)
	}
	if code, out, _ := run("list"); code != 0 || !strings.Contains(out, "web") || !strings.HasPrefix(out, "NAME") {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, _, e := run("start", "missing"); code != 1 || !strings.Contains(e, "not-found") {
		t.Fatalf("start of an unknown container: %d %q", code, e)
	}
	if code, _, _ := run("delete", "web"); code != 0 {
		t.Fatalf("delete: %d", code)
	}
}

// fakeSV is the smallest Supervisor ctl can drive.
type fakeSV struct {
	api.Supervisor
	specs map[string]spec.Spec
}

func (f *fakeSV) List() []supervisor.Status {
	out := []supervisor.Status{}
	for _, s := range f.specs {
		out = append(out, supervisor.Status{Spec: s, Desired: "stopped", Phase: supervisor.PhaseStopped})
	}
	return out
}

func (f *fakeSV) Get(name string) (supervisor.Status, error) {
	s, ok := f.specs[name]
	if !ok {
		return supervisor.Status{}, supervisor.ErrNotFound
	}
	return supervisor.Status{Spec: s}, nil
}

func (f *fakeSV) Put(s spec.Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	f.specs[s.Name] = s
	return nil
}

func (f *fakeSV) Start(name string) error {
	_, err := f.Get(name)
	return err
}

func (f *fakeSV) Delete(name string) error {
	if _, err := f.Get(name); err != nil {
		return err
	}
	delete(f.specs, name)
	return nil
}

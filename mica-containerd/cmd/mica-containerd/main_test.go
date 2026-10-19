package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/api"
	"github.com/micaoss/mica-podman/mica-containerd/internal/compose"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/store"
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
	if code := ctl(nil, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Fatalf("no command: %d %q", code, errOut.String())
	}
	if code := ctl([]string{"get"}, nil, &out, &errOut); code != 2 {
		t.Fatalf("get without a name: %d", code)
	}
	if code := ctl([]string{"--socket", "/nonexistent/api.sock", "list"}, nil, &out, &errOut); code != 1 {
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
	sv := &fakeSV{projects: map[string][]spec.Spec{}, dirs: map[string]string{}}
	srv := &http.Server{Handler: api.Handler(sv, api.Info{Version: "t"}), ReadHeaderTimeout: time.Second}
	go srv.Serve(l) //nolint:errcheck // closed below
	defer srv.Close()

	file := filepath.Join(dir, "web.yaml")
	_ = os.WriteFile(file, []byte("name: web\nservices:\n  app: {image: docker.io/library/nginx:1}\n"), 0o600)
	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("services:\n  app: {image: i, privileged: true}\n"), 0o600)
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := ctl(append([]string{"--socket", sock}, args...), strings.NewReader(""), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	if code, _, e := run("up", "-f", file); code != 0 || sv.dirs["web"] != dir {
		t.Fatalf("up -f, named by the file: %d %s %v", code, e, sv.dirs)
	}
	if code, _, e := run("up", "-p", "web", "-f", bad); code != 1 || !strings.Contains(e, `"problems"`) {
		t.Fatalf("a refused file: %d %s", code, e)
	}
	for _, cmd := range []string{"list", "projects"} {
		code, out, _ := run(cmd)
		if code != 0 || strings.Join(strings.Fields(out), " ") != "NAME PROJECT DESIRED PHASE RESTARTS EXIT IMAGE ERROR web-app-1 web stopped stopped 0 0 docker.io/library/nginx:1" {
			t.Fatalf("%s: %d %q", cmd, code, out)
		}
	}
	if code, _, e := run("start", "missing"); code != 1 || !strings.Contains(e, "not-found") {
		t.Fatalf("start of an unknown container: %d %q", code, e)
	}
	// In a project directory: its compose.yaml, the project named after it.
	proj := filepath.Join(dir, "My.Site")
	_ = os.Mkdir(proj, 0o755)
	_ = os.WriteFile(filepath.Join(proj, "compose.yaml"), []byte("services:\n  app: {image: i}\n"), 0o600)
	t.Chdir(proj)
	if code, _, e := run("up"); code != 0 || sv.dirs["mysite"] != proj {
		t.Fatalf("up in a project directory: %d %s %v", code, e, sv.dirs)
	}
	if code, _, e := run("down"); code != 0 || sv.projects["mysite"] != nil {
		t.Fatalf("down in a project directory: %d %s", code, e)
	}
	if code, _, _ := run("down", "-p", "web"); code != 0 || len(sv.projects) != 0 {
		t.Fatalf("down -p: %d", code)
	}
	for _, args := range [][]string{{"put", "web", file}, {"delete", "web"}, {"down", "-f", file}, {"up", "extra"}} {
		if code, _, _ := run(args...); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
	t.Chdir(dir)
	if code, _, e := run("up"); code != 2 || !strings.Contains(e, "no Compose file") {
		t.Fatalf("up without a file: %d %s", code, e)
	}
}

func TestComposeCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app-dir")
	_ = os.Mkdir(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  app: {image: i, volumes: [./data:/d]}\n"), 0o600)
	good := filepath.Join(dir, "web.yaml")
	_ = os.WriteFile(good, []byte("name: web\nservices:\n  app: {image: i}\n"), 0o600)
	var out, errOut bytes.Buffer
	if code := composeCheck([]string{good}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), `"project": "web"`) ||
		!strings.Contains(out.String(), `"name": "web-app-1"`) {
		t.Fatalf("a good file: %d %s %s", code, out.String(), errOut.String())
	}
	out.Reset()
	t.Chdir(dir)
	if code := composeCheck(nil, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), `"project": "app-dir"`) ||
		!strings.Contains(out.String(), `"host": "`+dir+`/data"`) || !strings.Contains(out.String(), `"projectDir": "`+dir+`"`) {
		t.Fatalf("the file of the working directory: %d %s %s", code, out.String(), errOut.String())
	}
	out.Reset()
	in := strings.NewReader("services:\n  app: {image: i, privileged: true}\n")
	if code := composeCheck([]string{"--project", "x", "-"}, in, &out, &errOut); code != 1 || out.Len() != 0 ||
		!strings.Contains(errOut.String(), `"error": "compose"`) || !strings.Contains(errOut.String(), `"field": "privileged"`) {
		t.Fatalf("a refused file: %d %s", code, errOut.String())
	}
	out.Reset()
	if code := composeCheck([]string{"--volumes", "/v", "-"}, strings.NewReader("services:\n  app: {image: i}\n"), &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), `"project": "app-dir"`) || !strings.Contains(out.String(), `"projectDir": "/v/app-dir"`) {
		t.Fatalf("stdin, named after the working directory, in the daemon's project directory: %d %s", code, out.String())
	}
	if code := composeCheck([]string{"-f", good, good}, nil, &out, &errOut); code != 2 {
		t.Fatalf("two files: %d", code)
	}
	t.Chdir(t.TempDir())
	if code := composeCheck(nil, nil, &out, &errOut); code != 2 {
		t.Fatalf("no file: %d", code)
	}
}

// fakeSV is the smallest Supervisor ctl can drive.
type fakeSV struct {
	api.Supervisor
	projects map[string][]spec.Spec
	dirs     map[string]string
}

func (f *fakeSV) status(s spec.Spec) supervisor.Status {
	return supervisor.Status{Project: s.Project, Service: s.Service, Spec: s, Desired: "stopped", Phase: supervisor.PhaseStopped}
}

func (f *fakeSV) List() []supervisor.Status {
	out := []supervisor.Status{}
	for _, specs := range f.projects {
		for _, s := range specs {
			out = append(out, f.status(s))
		}
	}
	return out
}

func (f *fakeSV) Projects() []supervisor.ProjectStatus {
	out := []supervisor.ProjectStatus{}
	for n, specs := range f.projects {
		p := supervisor.ProjectStatus{Name: n}
		for _, s := range specs {
			p.Services = append(p.Services, f.status(s))
		}
		out = append(out, p)
	}
	return out
}

func (f *fakeSV) Project(name string) (supervisor.ProjectStatus, error) {
	for _, p := range f.Projects() {
		if p.Name == name {
			return p, nil
		}
	}
	return supervisor.ProjectStatus{}, supervisor.ErrNoProject
}

func (f *fakeSV) PutProject(ctx context.Context, name string, p store.Project) error {
	specs, err := compose.Load(ctx, name, p.Compose, p.Dir, "/mica/volumes")
	if err != nil {
		return err
	}
	f.projects[name], f.dirs[name] = specs, p.Dir
	return nil
}

func (f *fakeSV) Get(string) (supervisor.Status, error) {
	return supervisor.Status{}, supervisor.ErrNotFound
}

func (f *fakeSV) Start(name string) error {
	_, err := f.Get(name)
	return err
}

func (f *fakeSV) DeleteProject(name string) error {
	if _, ok := f.projects[name]; !ok {
		return supervisor.ErrNoProject
	}
	delete(f.projects, name)
	return nil
}

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/compose"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/store"
	"github.com/micaoss/mica-podman/mica-containerd/internal/supervisor"
)

// stub is a Supervisor that records what it is asked.
type stub struct {
	mu      sync.Mutex
	specs   map[string]spec.Spec
	desired map[string]string
	dirs    map[string]string // the project directory each project was declared with
	changes chan supervisor.Change
}

func newStub() *stub {
	return &stub{specs: map[string]spec.Spec{}, desired: map[string]string{}, dirs: map[string]string{}, changes: make(chan supervisor.Change, 4)}
}

func (s *stub) List() []supervisor.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []supervisor.Status{}
	for n, sp := range s.specs {
		out = append(out, supervisor.Status{Project: sp.Project, Service: sp.Service, Spec: sp, Desired: s.desired[n], Phase: supervisor.PhaseStopped})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
	return out
}

func (s *stub) Get(name string) (supervisor.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.specs[name]
	if !ok {
		return supervisor.Status{}, supervisor.ErrNotFound
	}
	return supervisor.Status{Spec: sp, Desired: s.desired[name], Phase: supervisor.PhaseStopped}, nil
}

func (s *stub) put(sp spec.Spec) error {
	if err := sp.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs[sp.Name] = sp
	if s.desired[sp.Name] == "" {
		s.desired[sp.Name] = "stopped"
	}
	return nil
}

func (s *stub) PutProject(ctx context.Context, name string, p store.Project) error {
	specs, err := compose.Load(ctx, name, p.Compose, p.Dir, "/mica/volumes")
	if err != nil {
		return err
	}
	_ = s.DeleteProject(name)
	s.mu.Lock()
	s.dirs[name] = p.Dir
	s.mu.Unlock()
	for _, sp := range specs {
		if err := s.put(sp); err != nil {
			return err
		}
	}
	return nil
}

func (s *stub) PutProjects(ctx context.Context, files map[string]store.Project) error {
	for n, f := range files {
		if _, err := compose.Load(ctx, n, f.Compose, f.Dir, "/mica/volumes"); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.specs = map[string]spec.Spec{}
	s.mu.Unlock()
	for n, f := range files {
		if err := s.PutProject(ctx, n, f); err != nil {
			return err
		}
	}
	return nil
}

func (s *stub) DeleteProject(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for n, sp := range s.specs {
		if sp.Project == name {
			delete(s.specs, n)
			found = true
		}
	}
	if !found {
		return supervisor.ErrNoProject
	}
	return nil
}

func (s *stub) Projects() []supervisor.ProjectStatus {
	byName := map[string]*supervisor.ProjectStatus{}
	for _, st := range s.List() {
		p := byName[st.Spec.Project]
		if p == nil {
			p = &supervisor.ProjectStatus{Name: st.Spec.Project}
			byName[st.Spec.Project] = p
		}
		p.Services = append(p.Services, st)
	}
	out := []supervisor.ProjectStatus{}
	for _, p := range byName {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *stub) Project(name string) (supervisor.ProjectStatus, error) {
	for _, p := range s.Projects() {
		if p.Name == name {
			p.Compose = "services: ..."
			return p, nil
		}
	}
	return supervisor.ProjectStatus{}, supervisor.ErrNoProject
}

func (s *stub) set(name, desired string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.specs[name]; !ok {
		return supervisor.ErrNotFound
	}
	s.desired[name] = desired
	return nil
}

func (s *stub) Start(name string) error   { return s.set(name, "running") }
func (s *stub) Stop(name string) error    { return s.set(name, "stopped") }
func (s *stub) Restart(name string) error { return s.set(name, "running") }

func (s *stub) Logs(_ context.Context, name string, tail int, _ bool) (io.ReadCloser, error) {
	if _, err := s.Get(name); err != nil {
		return nil, err
	}
	lines := []string{"one", "two", "three"}
	if tail >= 0 && tail < len(lines) {
		lines = lines[len(lines)-tail:]
	}
	return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
}

func (s *stub) Subscribe() (<-chan supervisor.Change, func()) { return s.changes, func() {} }

func call(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m, rec.Body.String()
}

func TestProjectLifecycleOverTheAPI(t *testing.T) {
	sv := newStub()
	h := Handler(sv, Info{Version: "test"})
	code, m, raw := call(t, h, "PUT", "/v1/projects/web", "services:\n  app: {image: docker.io/library/nginx:1, ports: [\"8080:80\"]}\n")
	if code != 200 || m["name"] != "web" || m["compose"] == nil || !strings.Contains(raw, `"name": "web-app-1"`) {
		t.Fatalf("put: %d %s", code, raw)
	}
	if code, m, raw = call(t, h, "POST", "/v1/containers/web-app-1/start", ""); code != 200 || m["desired"] != "running" {
		t.Fatalf("start: %d %s", code, raw)
	}
	if code, m, raw = call(t, h, "POST", "/v1/containers/web-app-1/stop", ""); code != 200 || m["desired"] != "stopped" {
		t.Fatalf("stop: %d %s", code, raw)
	}
	if code, _, raw = call(t, h, "GET", "/v1/projects", ""); code != 200 || !strings.Contains(raw, `"project": "web"`) {
		t.Fatalf("projects: %d %s", code, raw)
	}
	if code, _, raw = call(t, h, "GET", "/v1/containers", ""); code != 200 || !strings.Contains(raw, `"service": "app"`) {
		t.Fatalf("list: %d %s", code, raw)
	}
	if code, _, _ = call(t, h, "DELETE", "/v1/projects/web", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, m, _ = call(t, h, "GET", "/v1/projects/web", ""); code != 404 || m["error"] != "not-found" {
		t.Fatalf("get after delete: %d %v", code, m)
	}
}

func TestARefusedComposeFileNamesEveryProblem(t *testing.T) {
	h := Handler(newStub(), Info{})
	code, m, raw := call(t, h, "PUT", "/v1/projects/web", "services:\n  app: {image: i, privileged: true, labels: {a: b}}\n")
	problems, _ := m["problems"].([]any)
	if code != 400 || m["error"] != "compose" || len(problems) != 2 || m["detail"] != "app: labels is not supported; app: privileged is not supported" {
		t.Fatalf("%d %s", code, raw)
	}
	if p, _ := problems[1].(map[string]any); p["service"] != "app" || p["field"] != "privileged" {
		t.Fatalf("problem %v", problems[0])
	}
}

func TestRefusalsNameTheirRule(t *testing.T) {
	h := Handler(newStub(), Info{})
	cases := []struct {
		method, path, body string
		code               int
		rule               string
	}{
		{"PUT", "/v1/projects/web", `services: [`, 400, "compose"},
		{"PUT", "/v1/projects", `{"projects": {"web": 1}}`, 400, "json"},
		{"PUT", "/v1/projects", `{"projects": {}} {}`, 400, "json"},
		{"PUT", "/v1/projects/Web", "services:\n  a: {image: i}\n", 400, "compose"},
		{"DELETE", "/v1/projects/none", ``, 404, "not-found"},
		{"POST", "/v1/containers/none/start", ``, 404, "not-found"},
		{"GET", "/v1/containers/none/logs", ``, 404, "not-found"},
		{"PUT", "/v1/containers/web", `{"image": "i"}`, 404, "route"},
		{"PUT", "/v1/containers", `{"containers": [{"name": "web", "image": "i"}]}`, 404, "route"},
		{"GET", "/v2/containers", ``, 404, "route"},
	}
	for _, c := range cases {
		code, m, raw := call(t, h, c.method, c.path, c.body)
		if code != c.code || m["error"] != c.rule {
			t.Errorf("%s %s %s: %d %s, want %d %s", c.method, c.path, c.body, code, raw, c.code, c.rule)
		}
	}
}

func TestPutProjectsReplacesTheSet(t *testing.T) {
	sv := newStub()
	h := Handler(sv, Info{})
	_, _, _ = call(t, h, "PUT", "/v1/projects/old", "services:\n  a: {image: i}\n")
	code, _, raw := call(t, h, "PUT", "/v1/projects", `{"projects": {"a": "services:\n  x: {image: i}\n", "b": "services:\n  y: {image: i}\n"}}`)
	if code != 200 || strings.Contains(raw, `"old"`) || !strings.Contains(raw, `"name": "b-y-1"`) {
		t.Fatalf("%d %s", code, raw)
	}
	// A project of a set may carry its project directory.
	code, _, raw = call(t, h, "PUT", "/v1/projects", `{"projects": {"a": {"compose": "services:\n  x: {image: i}\n", "projectDir": "/srv/a"}}}`)
	if code != 200 || sv.dirs["a"] != "/srv/a" {
		t.Fatalf("%d %s %v", code, raw, sv.dirs)
	}
	if code, m, _ := call(t, h, "PUT", "/v1/projects", `{"projects": {"a": {"compose": "", "dir": "/srv/a"}}}`); code != 400 || m["error"] != "json" {
		t.Fatalf("an unknown key: %d %v", code, m)
	}
}

func TestPutProjectTakesItsProjectDirectory(t *testing.T) {
	sv := newStub()
	h := Handler(sv, Info{})
	if code, _, raw := call(t, h, "PUT", "/v1/projects/web?projectDir=/srv/web", "services:\n  app: {image: i}\n"); code != 200 || sv.dirs["web"] != "/srv/web" {
		t.Fatalf("%d %s %v", code, raw, sv.dirs)
	}
	code, m, raw := call(t, h, "PUT", "/v1/projects/web?projectDir=srv/web", "services:\n  app: {image: i}\n")
	if problems, _ := m["problems"].([]any); code != 400 || len(problems) != 1 || !strings.Contains(raw, `"projectDir"`) {
		t.Fatalf("a relative project directory: %d %s", code, raw)
	}
}

func TestLogsTail(t *testing.T) {
	sv := newStub()
	h := Handler(sv, Info{})
	_ = sv.PutProject(context.Background(), "web", store.Project{Compose: []byte("services:\n  app: {image: i}\n")})
	if code, _, raw := call(t, h, "GET", "/v1/containers/web-app-1/logs?tail=2", ""); code != 200 || raw != "two\nthree\n" {
		t.Fatalf("%d %q", code, raw)
	}
	if code, m, _ := call(t, h, "GET", "/v1/containers/web-app-1/logs?tail=-3", ""); code != 400 || m["error"] != "tail" {
		t.Fatalf("a negative tail: %d %v", code, m)
	}
}

func TestStatus(t *testing.T) {
	sv := newStub()
	_ = sv.PutProject(context.Background(), "web", store.Project{Compose: []byte("services:\n  app: {image: i}\n")})
	code, m, _ := call(t, Handler(sv, Info{Version: "1.2"}), "GET", "/v1/status", "")
	if code != 200 || m["version"] != "1.2" || m["containers"] != float64(1) {
		t.Fatalf("%d %v", code, m)
	}
}

// The socket: 0600, a stale one replaced, and the API served through it.
func TestListenServesOnAPrivateSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "mcd") // short: a UNIX socket path is at most 107 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "run", "api.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o666); err != nil { // a stale socket left by a crash
		t.Fatal(err)
	}
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode %v %v", fi.Mode(), err)
	}
	sv := newStub()
	srv := &http.Server{Handler: Handler(sv, Info{}), ReadHeaderTimeout: time.Second}
	go srv.Serve(l) //nolint:errcheck // closed below
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	req, _ := http.NewRequest("PUT", "http://mica-containerd/v1/projects/web", strings.NewReader("services:\n  app: {image: i}\n"))
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("put over the socket: %v %v", resp, err)
	}
	resp.Body.Close()

	resp, err = client.Get("http://mica-containerd/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sv.changes <- supervisor.Change{Name: "web", Phase: supervisor.PhaseRunning}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, `"phase":"running"`) {
		t.Fatalf("event line %q %v", line, err)
	}
}

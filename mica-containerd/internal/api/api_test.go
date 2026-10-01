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

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/supervisor"
)

// stub is a Supervisor that records what it is asked.
type stub struct {
	mu      sync.Mutex
	specs   map[string]spec.Spec
	desired map[string]string
	changes chan supervisor.Change
}

func newStub() *stub {
	return &stub{specs: map[string]spec.Spec{}, desired: map[string]string{}, changes: make(chan supervisor.Change, 4)}
}

func (s *stub) List() []supervisor.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []supervisor.Status{}
	for n, sp := range s.specs {
		out = append(out, supervisor.Status{Spec: sp, Desired: s.desired[n], Phase: supervisor.PhaseStopped})
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

func (s *stub) Put(sp spec.Spec) error {
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

func (s *stub) PutAll(specs []spec.Spec) error {
	s.mu.Lock()
	s.specs = map[string]spec.Spec{}
	s.mu.Unlock()
	for _, sp := range specs {
		if err := s.Put(sp); err != nil {
			return err
		}
	}
	return nil
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

func (s *stub) Delete(name string) error {
	if err := s.set(name, ""); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.specs, name)
	s.mu.Unlock()
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

func TestContainerLifecycleOverTheAPI(t *testing.T) {
	sv := newStub()
	h := Handler(sv, Info{Version: "test"})
	code, m, raw := call(t, h, "PUT", "/v1/containers/web", `{"image": "docker.io/library/nginx:1", "publish": [{"host": 8080, "container": 80}]}`)
	if code != 200 || m["desired"] != "stopped" {
		t.Fatalf("put: %d %s", code, raw)
	}
	if code, m, raw = call(t, h, "POST", "/v1/containers/web/start", ""); code != 200 || m["desired"] != "running" {
		t.Fatalf("start: %d %s", code, raw)
	}
	if code, m, raw = call(t, h, "POST", "/v1/containers/web/stop", ""); code != 200 || m["desired"] != "stopped" {
		t.Fatalf("stop: %d %s", code, raw)
	}
	if code, _, raw = call(t, h, "GET", "/v1/containers", ""); code != 200 || !strings.Contains(raw, `"name": "web"`) {
		t.Fatalf("list: %d %s", code, raw)
	}
	if code, _, _ = call(t, h, "DELETE", "/v1/containers/web", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, m, _ = call(t, h, "GET", "/v1/containers/web", ""); code != 404 || m["error"] != "not-found" {
		t.Fatalf("get after delete: %d %v", code, m)
	}
}

func TestRefusalsNameTheirRule(t *testing.T) {
	h := Handler(newStub(), Info{})
	cases := []struct {
		method, path, body string
		code               int
		rule               string
	}{
		{"PUT", "/v1/containers/web", `{"image": ""}`, 400, "image"},
		{"PUT", "/v1/containers/web", `{"name": "other", "image": "i"}`, 400, "name"},
		{"PUT", "/v1/containers/web", `{"image": "i", "privileged": true}`, 400, "json"},
		{"PUT", "/v1/containers/web", `{"image": "i"} {"image": "j"}`, 400, "json"},
		{"PUT", "/v1/containers/Web", `{"image": "i"}`, 400, "name"},
		{"POST", "/v1/containers/none/start", ``, 404, "not-found"},
		{"GET", "/v1/containers/none/logs", ``, 404, "not-found"},
		{"GET", "/v2/containers", ``, 404, "route"},
	}
	for _, c := range cases {
		code, m, raw := call(t, h, c.method, c.path, c.body)
		if code != c.code || m["error"] != c.rule {
			t.Errorf("%s %s %s: %d %s, want %d %s", c.method, c.path, c.body, code, raw, c.code, c.rule)
		}
	}
}

func TestPutAllReplacesTheSet(t *testing.T) {
	sv := newStub()
	h := Handler(sv, Info{})
	_, _, _ = call(t, h, "PUT", "/v1/containers/old", `{"image": "i"}`)
	code, _, raw := call(t, h, "PUT", "/v1/containers", `{"containers": [{"name": "a", "image": "i"}, {"name": "b", "image": "i"}]}`)
	if code != 200 || strings.Contains(raw, `"old"`) || !strings.Contains(raw, `"name": "b"`) {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestLogsTail(t *testing.T) {
	sv := newStub()
	h := Handler(sv, Info{})
	_ = sv.Put(spec.Spec{Name: "web", Image: "i"})
	if code, _, raw := call(t, h, "GET", "/v1/containers/web/logs?tail=2", ""); code != 200 || raw != "two\nthree\n" {
		t.Fatalf("%d %q", code, raw)
	}
	if code, m, _ := call(t, h, "GET", "/v1/containers/web/logs?tail=-3", ""); code != 400 || m["error"] != "tail" {
		t.Fatalf("a negative tail: %d %v", code, m)
	}
}

func TestStatus(t *testing.T) {
	sv := newStub()
	_ = sv.Put(spec.Spec{Name: "web", Image: "i"})
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
	req, _ := http.NewRequest("PUT", "http://mica-containerd/v1/containers/web", strings.NewReader(`{"image": "i"}`))
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

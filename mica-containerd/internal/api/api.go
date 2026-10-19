// Package api is mica-containerd's HTTP API, served on a UNIX socket only: the
// socket's mode (0600, root) is the trust boundary, so there is no authentication
// above it. Bodies are JSON but a project's Compose file, which is YAML; an error is
// {"error": "<rule>", "detail": "..."}, with "problems" for a refused Compose file.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/compose"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/store"
	"github.com/micaoss/mica-podman/mica-containerd/internal/supervisor"
)

// Supervisor is what the API drives.
type Supervisor interface {
	List() []supervisor.Status
	Get(name string) (supervisor.Status, error)
	Projects() []supervisor.ProjectStatus
	Project(name string) (supervisor.ProjectStatus, error)
	PutProject(ctx context.Context, name string, p store.Project) error
	PutProjects(ctx context.Context, projects map[string]store.Project) error
	DeleteProject(name string) error
	Start(name string) error
	Stop(name string) error
	Restart(name string) error
	Logs(ctx context.Context, name string, tail int, follow bool) (io.ReadCloser, error)
	Subscribe() (<-chan supervisor.Change, func())
}

// Info is what GET /v1/status reports besides the containers.
type Info struct {
	Version string `json:"version"`
	Store   string `json:"store"`
	BootID  string `json:"boot_id"`
	Started time.Time
}

// maxBody bounds a request body.
const maxBody = 1 << 20

// Handler serves the v1 API.
func Handler(sv Supervisor, info Info) http.Handler {
	h := &handler{sv: sv, info: info}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", h.status)
	mux.HandleFunc("GET /v1/projects", h.projects)
	mux.HandleFunc("PUT /v1/projects", h.putProjects)
	mux.HandleFunc("GET /v1/projects/{project}", h.project)
	mux.HandleFunc("PUT /v1/projects/{project}", h.putProject)
	mux.HandleFunc("DELETE /v1/projects/{project}", h.deleteProject)
	mux.HandleFunc("GET /v1/containers", h.list)
	mux.HandleFunc("GET /v1/containers/{name}", h.get)
	mux.HandleFunc("POST /v1/containers/{name}/start", h.verb(sv.Start))
	mux.HandleFunc("POST /v1/containers/{name}/stop", h.verb(sv.Stop))
	mux.HandleFunc("POST /v1/containers/{name}/restart", h.verb(sv.Restart))
	mux.HandleFunc("GET /v1/containers/{name}/logs", h.logs)
	mux.HandleFunc("GET /v1/events", h.events)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fail(w, http.StatusNotFound, "route", "no such endpoint")
	})
	return mux
}

type handler struct {
	sv   Supervisor
	info Info
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v) //nolint:errcheck // the client went away; nothing to tell it
}

func fail(w http.ResponseWriter, code int, rule, detail string) {
	reply(w, code, map[string]string{"error": rule, "detail": detail})
}

// failFor answers err by what it is: a refused Compose file or spec, an unknown name,
// or ours.
func failFor(w http.ResponseWriter, err error) {
	var se *spec.Error
	var ce *compose.Error
	switch {
	case errors.As(err, &ce):
		reply(w, http.StatusBadRequest, map[string]any{"error": "compose", "detail": ce.Error(), "problems": ce.Problems})
	case errors.As(err, &se):
		fail(w, http.StatusBadRequest, se.Rule, se.Detail)
	case errors.Is(err, supervisor.ErrNotFound), errors.Is(err, supervisor.ErrNoProject):
		fail(w, http.StatusNotFound, "not-found", err.Error())
	default:
		fail(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "json", err.Error())
		return false
	}
	if dec.More() {
		fail(w, http.StatusBadRequest, "json", "more than one JSON value")
		return false
	}
	return true
}

func (h *handler) status(w http.ResponseWriter, _ *http.Request) {
	list := h.sv.List()
	phases := map[supervisor.Phase]int{}
	for _, s := range list {
		phases[s.Phase]++
	}
	reply(w, http.StatusOK, map[string]any{
		"version": h.info.Version, "store": h.info.Store, "boot_id": h.info.BootID,
		"started": h.info.Started, "containers": len(list), "phases": phases,
	})
}

func (h *handler) list(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{"containers": h.sv.List()})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	st, err := h.sv.Get(r.PathValue("name"))
	if err != nil {
		failFor(w, err)
		return
	}
	reply(w, http.StatusOK, st)
}

func (h *handler) projects(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{"projects": h.sv.Projects()})
}

func (h *handler) project(w http.ResponseWriter, r *http.Request) {
	p, err := h.sv.Project(r.PathValue("project"))
	if err != nil {
		failFor(w, err)
		return
	}
	reply(w, http.StatusOK, p)
}

func (h *handler) putProject(w http.ResponseWriter, r *http.Request) {
	file, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		fail(w, http.StatusBadRequest, "body", err.Error())
		return
	}
	p := store.Project{Compose: file, Dir: r.URL.Query().Get("projectDir")}
	if err := h.sv.PutProject(r.Context(), r.PathValue("project"), p); err != nil {
		failFor(w, err)
		return
	}
	h.project(w, r)
}

func (h *handler) putProjects(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Projects map[string]declared `json:"projects"`
	}
	if !decode(w, r, &body) {
		return
	}
	projects := make(map[string]store.Project, len(body.Projects))
	for n, d := range body.Projects {
		projects[n] = store.Project(d)
	}
	if err := h.sv.PutProjects(r.Context(), projects); err != nil {
		failFor(w, err)
		return
	}
	h.projects(w, r)
}

// declared is a project of a set: its Compose file, or {"compose": <file>,
// "projectDir": <its project directory>}.
type declared store.Project

func (d *declared) UnmarshalJSON(b []byte) error {
	var file string
	if err := json.Unmarshal(b, &file); err == nil {
		*d = declared{Compose: []byte(file)}
		return nil
	}
	var o struct {
		Compose    string `json:"compose"`
		ProjectDir string `json:"projectDir"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return err
	}
	*d = declared{Compose: []byte(o.Compose), Dir: o.ProjectDir}
	return nil
}

func (h *handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := h.sv.DeleteProject(r.PathValue("project")); err != nil {
		failFor(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) verb(do func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := do(r.PathValue("name")); err != nil {
			failFor(w, err)
			return
		}
		h.get(w, r)
	}
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	tail := -1
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, "tail", fmt.Sprintf("%q is not a number of lines", v))
			return
		}
		tail = n
	}
	follow := r.URL.Query().Get("follow") == "1" || r.URL.Query().Get("follow") == "true"
	rc, err := h.sv.Logs(r.Context(), r.PathValue("name"), tail, follow)
	if err != nil {
		failFor(w, err)
		return
	}
	defer rc.Close() //nolint:errcheck // a pipe
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	changes, cancel := h.sv.Subscribe()
	defer cancel()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for {
		select {
		case <-r.Context().Done():
			return
		case c, ok := <-changes:
			if !ok {
				return
			}
			if enc.Encode(c) != nil || bw.Flush() != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// Listen opens the socket at path: its directory made 0755 when missing, a stale
// socket replaced, the socket itself 0600 before anyone can connect through a wider
// mode.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // /run/mica-containerd: the socket's own mode is the boundary
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	old := umask(0o177)
	l, err := net.Listen("unix", path)
	umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close() //nolint:errcheck,gosec // the chmod already failed
		return nil, err
	}
	return l, nil
}

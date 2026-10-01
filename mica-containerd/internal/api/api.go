// Package api is mica-containerd's HTTP API, served on a UNIX socket only: the
// socket's mode (0600, root) is the trust boundary, so there is no authentication
// above it. Bodies are JSON; an error is {"error": "<rule>", "detail": "..."}.
package api

import (
	"bufio"
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

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/supervisor"
)

// Supervisor is what the API drives.
type Supervisor interface {
	List() []supervisor.Status
	Get(name string) (supervisor.Status, error)
	Put(s spec.Spec) error
	PutAll(specs []spec.Spec) error
	Delete(name string) error
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
	mux.HandleFunc("GET /v1/containers", h.list)
	mux.HandleFunc("PUT /v1/containers", h.putAll)
	mux.HandleFunc("GET /v1/containers/{name}", h.get)
	mux.HandleFunc("PUT /v1/containers/{name}", h.put)
	mux.HandleFunc("DELETE /v1/containers/{name}", h.delete)
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

// failFor answers err by what it is: a refused spec, an unknown name, or ours.
func failFor(w http.ResponseWriter, err error) {
	var se *spec.Error
	switch {
	case errors.As(err, &se):
		fail(w, http.StatusBadRequest, se.Rule, se.Detail)
	case errors.Is(err, supervisor.ErrNotFound):
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

func (h *handler) put(w http.ResponseWriter, r *http.Request) {
	var s spec.Spec
	if !decode(w, r, &s) {
		return
	}
	name := r.PathValue("name")
	if s.Name == "" {
		s.Name = name
	}
	if s.Name != name {
		fail(w, http.StatusBadRequest, "name", fmt.Sprintf("the body names %q and the path %q", s.Name, name))
		return
	}
	if err := h.sv.Put(s); err != nil {
		failFor(w, err)
		return
	}
	h.get(w, r)
}

func (h *handler) putAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Containers []spec.Spec `json:"containers"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := h.sv.PutAll(body.Containers); err != nil {
		failFor(w, err)
		return
	}
	h.list(w, r)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.sv.Delete(r.PathValue("name")); err != nil {
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

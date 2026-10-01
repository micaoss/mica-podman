package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
)

// Podman drives the podman CLI with JSON output: no resident service, no libpod.
type Podman struct {
	// Bin is the podman binary.
	Bin string
	// LogDir holds each container's log, <LogDir>/<name>.log: on /run, so logs cost
	// RAM and never flash, on either init.
	LogDir string
	// LogMaxSize caps one log, in podman's size syntax.
	LogMaxSize string
}

// RunArgs are the arguments of `podman run` for s. podman's own restart policy is
// off: restarting is the supervisor's.
func (p *Podman) RunArgs(s spec.Spec) []string {
	s = s.Normalized()
	args := []string{
		"run", "--detach", "--replace", "--name", s.Name,
		"--label", LabelManaged + "=1",
		"--label", LabelSpec + "=" + s.Hash(),
		"--restart", "no",
		"--log-driver", "k8s-file",
		"--log-opt", "path=" + filepath.Join(p.LogDir, s.Name+".log"),
		"--log-opt", "max-size=" + p.LogMaxSize,
	}
	keys := make([]string, 0, len(s.Environment))
	for k := range s.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+s.Environment[k])
	}
	for _, port := range s.Publish {
		args = append(args, "--publish", fmt.Sprintf("%d:%d/%s", port.Host, port.Container, port.Protocol))
	}
	for _, v := range s.Volumes {
		mount := v.Host + ":" + v.Container
		if v.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "--volume", mount)
	}
	if s.Limits.Pids > 0 {
		args = append(args, "--pids-limit", strconv.FormatInt(s.Limits.Pids, 10))
	}
	if s.Limits.Memory != "" {
		args = append(args, "--memory", s.Limits.Memory)
	}
	if s.Limits.CPU != "" {
		args = append(args, "--cpus", s.Limits.CPU)
	}
	if h := s.Health; h != nil {
		command, err := json.Marshal(h.Command)
		if err != nil {
			panic(err) // a list of strings
		}
		// "disable": podman schedules nothing (on systemd it would add a timer); the
		// supervisor runs the check on the interval.
		args = append(args, "--health-cmd", string(command), "--health-interval", "disable",
			"--health-timeout", fmt.Sprintf("%ds", h.TimeoutSeconds),
			"--health-retries", strconv.Itoa(h.Retries),
			"--health-start-period", fmt.Sprintf("%ds", h.StartPeriodSeconds))
	}
	return append(append(args, s.Image), s.Command...)
}

func (p *Podman) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.Bin, args...) //nolint:gosec // the binary is configured; the arguments are an argv, never a shell line
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("podman %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// psEntry is the part of `podman ps --format json` read here.
type psEntry struct {
	ID        string `json:"Id"`
	Names     []string
	ImageID   string
	Labels    map[string]string
	State     string
	ExitCode  int32
	StartedAt int64
	ExitedAt  int64
}

// ParsePS reads `podman ps --all --format json`.
func ParsePS(b []byte) ([]Container, error) {
	var entries []psEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("podman ps: %w", err)
	}
	out := make([]Container, 0, len(entries))
	for _, e := range entries {
		if len(e.Names) == 0 {
			continue
		}
		c := Container{ID: e.ID, Name: e.Names[0], ImageID: e.ImageID, Labels: e.Labels, State: e.State, ExitCode: int(e.ExitCode)}
		if e.StartedAt > 0 {
			c.StartedAt = time.Unix(e.StartedAt, 0)
		}
		if e.ExitedAt > 0 {
			c.ExitedAt = time.Unix(e.ExitedAt, 0)
		}
		out = append(out, c)
	}
	return out, nil
}

// List implements Engine.
func (p *Podman) List(ctx context.Context) ([]Container, error) {
	out, err := p.run(ctx, "ps", "--all", "--filter", "label="+LabelManaged+"=1", "--format", "json")
	if err != nil {
		return nil, err
	}
	return ParsePS(out)
}

// Run implements Engine.
func (p *Podman) Run(ctx context.Context, s spec.Spec) error {
	_, err := p.run(ctx, p.RunArgs(s)...)
	return err
}

// Start implements Engine.
func (p *Podman) Start(ctx context.Context, name string) error {
	_, err := p.run(ctx, "start", name)
	return err
}

// Stop implements Engine.
func (p *Podman) Stop(ctx context.Context, name string, timeout time.Duration) error {
	_, err := p.run(ctx, "stop", "--time", strconv.Itoa(int(timeout.Seconds())), name)
	return err
}

// Remove implements Engine.
func (p *Podman) Remove(ctx context.Context, name string) error {
	_, err := p.run(ctx, "rm", "--force", "--ignore", name)
	return err
}

// HealthCheck implements Engine: `podman healthcheck run` exits 0 on a pass and 1 on
// a failure; anything else is an error.
func (p *Podman) HealthCheck(ctx context.Context, name string) (bool, error) {
	_, err := p.run(ctx, "healthcheck", "run", name)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	default:
		return false, err
	}
}

// Logs implements Engine: `podman logs`, stdout and stderr interleaved as written.
func (p *Podman) Logs(ctx context.Context, name string, tail int, follow bool) (io.ReadCloser, error) {
	args := []string{"logs", "--tail", strconv.Itoa(tail)}
	if follow {
		args = append(args, "--follow")
	}
	cmd := exec.CommandContext(ctx, p.Bin, append(args, name)...) //nolint:gosec // as in run
	r, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("podman logs: %w", err)
	}
	go func() { w.CloseWithError(cmd.Wait()) }()
	return r, nil
}

// eventEntry is the part of `podman events --format json` read here.
type eventEntry struct {
	Name              string
	Status            string
	Type              string
	ContainerExitCode *int  `json:",omitempty"`
	TimeNano          int64 `json:"timeNano"`
}

// ParseEvent reads one line of `podman events --format json`; ok is false for a
// line that is no container event.
func ParseEvent(line []byte) (Event, bool, error) {
	var e eventEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return Event{}, false, fmt.Errorf("podman events: %w", err)
	}
	if e.Type != "container" || e.Name == "" {
		return Event{}, false, nil
	}
	return Event{Name: e.Name, Status: e.Status, ExitCode: e.ContainerExitCode, Time: time.Unix(0, e.TimeNano)}, true, nil
}

// Events implements Engine.
func (p *Podman) Events(ctx context.Context) (<-chan Event, <-chan error) {
	events := make(chan Event)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		cmd := exec.CommandContext(ctx, p.Bin, "events", "--format", "json", //nolint:gosec // as in run
			"--filter", "type=container", "--filter", "label="+LabelManaged+"=1")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errs <- err
			return
		}
		if err := cmd.Start(); err != nil {
			errs <- fmt.Errorf("podman events: %w", err)
			return
		}
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			e, ok, err := ParseEvent(scanner.Bytes())
			if err != nil || !ok {
				continue
			}
			select {
			case events <- e:
			case <-ctx.Done():
			}
		}
		err = cmd.Wait()
		if ctx.Err() == nil {
			errs <- fmt.Errorf("podman events ended: %v", err)
		}
	}()
	return events, errs
}

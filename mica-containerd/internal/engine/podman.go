package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
func (p *Podman) RunArgs(s spec.Spec) []string { return p.runArgs(s, s.Devices) }

// runArgs are RunArgs with devices for s's: its own, with each host link resolved.
func (p *Podman) runArgs(s spec.Spec, devices []spec.Device) []string {
	s = s.Normalized()
	args := []string{
		"run", "--detach", "--replace", "--name", s.Name,
		"--label", LabelManaged + "=1",
		"--label", LabelSpec + "=" + s.Hash(),
		"--label", LabelProject + "=" + s.Project,
		"--restart", "no",
		"--log-driver", "k8s-file",
		"--log-opt", "path=" + filepath.Join(p.LogDir, s.Name+".log"),
		"--log-opt", "max-size=" + p.LogMaxSize,
	}
	if s.NetworkMode != "" {
		args = append(args, "--network", s.NetworkMode)
	} else {
		args = append(args, "--network", s.Network)
	}
	for _, a := range s.Aliases {
		args = append(args, "--network-alias", a)
	}
	for _, c := range s.CapAdd {
		args = append(args, "--cap-add", c)
	}
	for _, c := range s.CapDrop {
		args = append(args, "--cap-drop", c)
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	if s.WorkingDir != "" {
		args = append(args, "--workdir", s.WorkingDir)
	}
	if len(s.Entrypoint) > 0 {
		entrypoint, err := json.Marshal(s.Entrypoint)
		if err != nil {
			panic(err) // a list of strings
		}
		// podman reads a JSON array as the exec form, one argument per element.
		args = append(args, "--entrypoint", string(entrypoint))
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
	for _, d := range devices {
		args = append(args, "--device", d.Host+":"+d.Container+":"+d.Permissions)
	}
	for _, r := range s.DeviceCgroupRules {
		args = append(args, "--device-cgroup-rule", r)
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
	Image     string
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
		c := Container{ID: e.ID, Name: e.Names[0], Image: e.Image, ImageID: e.ImageID, Labels: e.Labels, State: e.State, ExitCode: int(e.ExitCode)}
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
	out, err := p.run(ctx, "ps", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	return ParsePS(out)
}

// Run implements Engine.
func (p *Podman) Run(ctx context.Context, s spec.Spec) error {
	for _, v := range s.Volumes {
		if v.Make {
			if err := os.MkdirAll(v.Host, 0o755); err != nil { //nolint:gosec // a volume directory the container's own users read
				return err
			}
		}
	}
	// A device is found as it is now: a link such as /dev/serial/by-id/... names the
	// node the device has since it was plugged in.
	devices := make([]spec.Device, len(s.Devices))
	for i, d := range s.Devices {
		node, err := filepath.EvalSymlinks(d.Host)
		if err != nil {
			return fmt.Errorf("device %s is absent: %w", d.Host, err)
		}
		d.Host = node
		devices[i] = d
	}
	_, err := p.run(ctx, p.runArgs(s, devices)...)
	return err
}

// netEntry is the part of `podman network ls --format json` read here.
type netEntry struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

// ParseNetworks reads `podman network ls --format json`.
func ParseNetworks(b []byte) ([]Network, error) {
	var entries []netEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("podman network ls: %w", err)
	}
	out := make([]Network, 0, len(entries))
	for _, e := range entries {
		out = append(out, Network{Name: e.Name, Project: e.Labels[LabelProject]})
	}
	return out, nil
}

// Networks implements Engine.
func (p *Podman) Networks(ctx context.Context) ([]Network, error) {
	out, err := p.run(ctx, "network", "ls", "--filter", "label="+LabelManaged+"=1", "--format", "json")
	if err != nil {
		return nil, err
	}
	return ParseNetworks(out)
}

// CreateNetwork implements Engine: a bridge with podman's name resolution.
func (p *Podman) CreateNetwork(ctx context.Context, name, project string) error {
	_, err := p.run(ctx, "network", "create", "--ignore",
		"--label", LabelManaged+"=1", "--label", LabelProject+"="+project, name)
	return err
}

// RemoveNetwork implements Engine.
func (p *Podman) RemoveNetwork(ctx context.Context, name string) error {
	if _, err := p.run(ctx, "network", "exists", name); err != nil {
		return nil //nolint:nilerr // exit 1: there is no such network
	}
	_, err := p.run(ctx, "network", "rm", name)
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

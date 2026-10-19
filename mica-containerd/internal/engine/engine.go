// Package engine is what the supervisor asks of the container engine, and its podman
// implementation. Every container and network the supervisor creates carries
// LabelManaged, a container LabelSpec with the hash of the spec it was created from,
// and both LabelProject with the Compose project they are of. A container without
// LabelManaged was created by someone else.
package engine

import (
	"context"
	"io"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
)

// The labels of a managed container.
const (
	LabelManaged = "mica.containerd"
	LabelSpec    = "mica.containerd.spec"
	LabelProject = "mica.containerd.project"
)

// Container is what the engine reports of one container.
type Container struct {
	ID        string
	Name      string
	Image     string
	ImageID   string
	Labels    map[string]string
	State     string // podman's: created, running, exited, stopped, paused, ...
	ExitCode  int
	StartedAt time.Time
	ExitedAt  time.Time
}

// Running reports whether the container's process is up.
func (c Container) Running() bool { return c.State == "running" || c.State == "paused" }

// Managed reports whether the supervisor created the container.
func (c Container) Managed() bool { return c.Labels[LabelManaged] == "1" }

// Network is one network the supervisor created, for a project.
type Network struct {
	Name    string
	Project string
}

// Event is one container event.
type Event struct {
	Name     string
	Status   string // podman's: start, died, remove, health_status, ...
	ExitCode *int
	Time     time.Time
}

// Engine creates, starts, stops and removes containers, and reports them.
type Engine interface {
	// List reports every container, managed or not.
	List(ctx context.Context) ([]Container, error)
	// Run creates the container of s, replacing one of the same name, and starts it;
	// the host directories of the volumes s marks Make are created first.
	Run(ctx context.Context, s spec.Spec) error
	// Start starts an existing container.
	Start(ctx context.Context, name string) error
	// Stop stops a container, killing it after timeout.
	Stop(ctx context.Context, name string, timeout time.Duration) error
	// Remove removes a container, stopping it first; a missing one is not an error.
	Remove(ctx context.Context, name string) error
	// HealthCheck runs a container's health check once: true when it passes.
	HealthCheck(ctx context.Context, name string) (bool, error)
	// Logs reads a container's log: its last tail lines (all when tail < 0), and what
	// follows when follow is set, until ctx ends.
	Logs(ctx context.Context, name string, tail int, follow bool) (io.ReadCloser, error)
	// Networks reports every managed network.
	Networks(ctx context.Context) ([]Network, error)
	// CreateNetwork creates the network of project unless it exists.
	CreateNetwork(ctx context.Context, name, project string) error
	// RemoveNetwork removes a network; a missing one is not an error.
	RemoveNetwork(ctx context.Context, name string) error
	// Events streams the events of managed containers until ctx ends or the stream
	// breaks; the error channel then carries why, and both close.
	Events(ctx context.Context) (<-chan Event, <-chan error)
}

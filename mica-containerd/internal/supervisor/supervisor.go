// Package supervisor keeps every declared container in the state it was declared in.
//
// What it decides from is persisted or observed, never memory alone: the desired state
// is in the store, and whether a stopped container is restarted follows from its
// restart policy and the exit code and times podman reports. So a restart of the
// supervisor neither restarts a container someone stopped nor forgets one that died
// while it was down. Events trigger a pass, and a periodic full pass repairs anything
// an event missed.
//
// Containers are declared as Compose projects: each service of a project is one
// container, on the project's network. A container without the managed label is
// never touched, and a service whose name one holds waits for it to go.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/compose"
	"github.com/micaoss/mica-podman/mica-containerd/internal/engine"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/store"
)

// Phase is what the supervisor observes of a declared container.
type Phase string

// The phases.
const (
	PhaseStopped  Phase = "stopped"  // desired stopped, or never started
	PhaseStarting Phase = "starting" // started, its run not yet confirmed
	PhaseRunning  Phase = "running"
	PhaseBackoff  Phase = "backoff" // stopped on its own; restarting after a gap
	PhaseExited   Phase = "exited"  // stopped on its own; its policy leaves it stopped
	PhaseFatal    Phase = "fatal"   // restarts exhausted; stays until started again
	PhaseWaiting  Phase = "waiting" // to start, once its dependencies are ready
)

// The health states.
const (
	HealthStarting  = "starting"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
)

// Status is one declared container as the API reports it.
type Status struct {
	Project       string    `json:"project"`
	Service       string    `json:"service"`
	Spec          spec.Spec `json:"spec"`
	Desired       string    `json:"desired"`
	Phase         Phase     `json:"phase"`
	Health        string    `json:"health,omitempty"`
	Exists        bool      `json:"exists"`
	ImageID       string    `json:"image_id,omitempty"`
	StartedAt     time.Time `json:"started_at,omitzero"`
	ExitedAt      time.Time `json:"exited_at,omitzero"`
	ExitCode      int       `json:"exit_code"`
	StartAttempts int       `json:"start_attempts"`
	Restarts      int       `json:"restarts"`
	NextAttempt   time.Time `json:"next_attempt,omitzero"`
	Error         string    `json:"error,omitempty"`
}

// ProjectStatus is one project as the API reports it: its services' containers, its
// project directory, and the Compose file it was declared from when asked for one
// project.
type ProjectStatus struct {
	Name       string   `json:"name"`
	ProjectDir string   `json:"projectDir,omitempty"`
	Compose    string   `json:"compose,omitempty"`
	Services   []Status `json:"services"`
}

// Options configure a Supervisor.
type Options struct {
	// BootID names this boot (/proc/sys/kernel/random/boot_id); a new one makes every
	// container desired running again.
	BootID string
	// BootTime is when this boot began; a container not started since is started, not
	// judged by its restart policy.
	BootTime time.Time
	// Resync is the period of the full pass.
	Resync time.Duration
	// StopTimeout is how long a stopping container gets before it is killed.
	StopTimeout time.Duration
	// Volumes holds each project's named volumes, and its relative host paths when it
	// was declared without a project directory.
	Volumes string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Log receives what the supervisor does.
	Log *slog.Logger
}

// tracking is the supervisor's memory of one container, rebuilt from facts after a
// restart: tallies and the exit it last decided on.
type tracking struct {
	phase         Phase
	startAttempts int
	restarts      int
	nextAttempt   time.Time
	handledExit   time.Time
	force         bool // an explicit start: start now, tallies reset
	restart       bool // an explicit restart: stop a running container, then start it
	err           string

	health     string    // of the current run; "" without a check or a run
	failures   int       // health check failures in a row
	nextHealth time.Time // when the next check is due
	healthRun  time.Time // the start of the run the health state is about
}

// Change is one container's phase moving, as /v1/events streams it.
type Change struct {
	Name     string    `json:"name"`
	Phase    Phase     `json:"phase"`
	Desired  string    `json:"desired"`
	ExitCode int       `json:"exit_code"`
	Time     time.Time `json:"time"`
}

// Supervisor owns every managed container.
//
// mu guards the declarations and what the supervisor knows of them; it is never held
// across an engine call, so the API does not wait for a container to stop. passMu
// keeps passes one at a time.
type Supervisor struct {
	store  *store.Store
	engine engine.Engine
	opts   Options

	passMu sync.Mutex

	mu       sync.Mutex
	records  map[string]store.Record
	track    map[string]*tracking
	observed map[string]engine.Container
	foreign  map[string]engine.Container // by name: the containers without the managed label
	projects map[string]store.Project    // what each project was declared from
	changed  map[string]uint64           // per name, the seq of the last API call that changed it
	seq      uint64
	wake     chan struct{}

	subMu     sync.Mutex
	subs      map[chan Change]struct{}
	lastPhase map[string]Phase
}

// New loads the store and brings it to this boot.
func New(st *store.Store, en engine.Engine, opts Options) (*Supervisor, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Resync <= 0 {
		opts.Resync = time.Minute
	}
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = 10 * time.Second
	}
	if opts.Volumes == "" {
		opts.Volumes = "/mica/volumes"
	}
	records, bad, err := st.Load()
	if err != nil {
		return nil, err
	}
	for _, e := range bad {
		opts.Log.Error("a stored declaration is not loaded; it stays on disk", "error", e)
	}
	projects, err := st.Projects()
	if err != nil {
		return nil, err
	}
	s := &Supervisor{store: st, engine: en, opts: opts, records: records, projects: projects, foreign: map[string]engine.Container{},
		track: map[string]*tracking{}, observed: map[string]engine.Container{}, changed: map[string]uint64{}, wake: make(chan struct{}, 1),
		subs: map[chan Change]struct{}{}, lastPhase: map[string]Phase{}}
	last, err := st.BootID()
	if err != nil {
		return nil, err
	}
	if opts.BootID != "" && last != opts.BootID {
		for name, r := range records {
			r.Desired = store.Running
			if err := st.Put(r); err != nil {
				return nil, err
			}
			records[name] = r
		}
		if err := st.SetBootID(opts.BootID); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Supervisor) tr(name string) *tracking {
	t := s.track[name]
	if t == nil {
		t = &tracking{}
		s.track[name] = t
	}
	return t
}

// touch marks name changed by an API call and wakes a pass. Called with mu held.
func (s *Supervisor) touch(name string) {
	s.seq++
	s.changed[name] = s.seq
	s.poke()
}

// unlocked runs an engine call about name without holding mu, which is held around
// it. It reports false when an API call changed name meanwhile: the pass then
// stores nothing of name, and the pass the API call woke decides on it afresh.
func (s *Supervisor) unlocked(name string, call func()) bool {
	seen := s.changed[name]
	s.mu.Unlock()
	call()
	s.mu.Lock()
	return s.changed[name] == seen
}

func (s *Supervisor) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ErrNotFound is a name no declaration carries.
var ErrNotFound = errors.New("no such container")

// ErrNoProject is a name no project carries.
var ErrNoProject = errors.New("no such project")

// put stores one validated spec: a new container is desired running, a replaced one
// keeps its desired state. Called with mu held.
func (s *Supervisor) put(sp spec.Spec) error {
	r, ok := s.records[sp.Name]
	if !ok {
		r.Desired = store.Running
	}
	r.Spec = sp
	if err := s.store.Put(r); err != nil {
		return err
	}
	s.records[sp.Name] = r
	if t := s.track[sp.Name]; t != nil && t.phase == PhaseFatal {
		*t = tracking{force: true}
	}
	s.touch(sp.Name)
	return nil
}

// PutProject declares one project from its Compose file, or replaces it: its services
// are declared and the containers of services it no longer has are removed.
func (s *Supervisor) PutProject(ctx context.Context, name string, p store.Project) error {
	specs, err := compose.Load(ctx, name, p.Compose, p.Dir, s.opts.Volumes)
	if err != nil {
		return err
	}
	if err := s.unmanaged(ctx, specs); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sp := range specs {
		if r, ok := s.records[sp.Name]; ok && r.Spec.Project != name {
			return &spec.Error{Rule: "name", Detail: fmt.Sprintf("container %s is declared by project %s", sp.Name, r.Spec.Project)}
		}
	}
	return s.declare(name, p, specs)
}

// unmanaged refuses a container name that a container without the managed label
// holds: that container is never replaced. Called without mu.
func (s *Supervisor) unmanaged(ctx context.Context, specs []spec.Spec) error {
	listed, err := s.engine.List(ctx)
	if err != nil {
		return err
	}
	held := map[string]engine.Container{}
	for _, c := range listed {
		if !c.Managed() {
			held[c.Name] = c
		}
	}
	for _, sp := range specs {
		if c, ok := held[sp.Name]; ok {
			return &spec.Error{Rule: "name", Detail: fmt.Sprintf("container %s is held by container %.12s, which mica-containerd does not manage", sp.Name, c.ID)}
		}
	}
	return nil
}

// PutProjects declares exactly the projects of files: every other project is removed.
// One refused file refuses the set.
func (s *Supervisor) PutProjects(ctx context.Context, files map[string]store.Project) error {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	loaded := map[string][]spec.Spec{}
	owners := map[string]string{}
	var all []spec.Spec
	for _, n := range names {
		specs, err := compose.Load(ctx, n, files[n].Compose, files[n].Dir, s.opts.Volumes)
		var ce *compose.Error
		if errors.As(err, &ce) {
			for i := range ce.Problems {
				ce.Problems[i].Project = n
			}
		}
		if err != nil {
			return err
		}
		for _, sp := range specs {
			if o, ok := owners[sp.Name]; ok {
				return &spec.Error{Rule: "name", Detail: fmt.Sprintf("container %s is declared by projects %s and %s", sp.Name, o, n)}
			}
			owners[sp.Name] = n
		}
		loaded[n] = specs
		all = append(all, specs...)
	}
	if err := s.unmanaged(ctx, all); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, r := range s.records {
		if _, kept := loaded[r.Spec.Project]; !kept || owners[name] != r.Spec.Project {
			if err := s.delete(name); err != nil {
				return err
			}
		}
	}
	for n := range s.projects {
		if _, kept := files[n]; !kept {
			if err := s.store.DeleteProject(n); err != nil {
				return err
			}
			delete(s.projects, n)
		}
	}
	for _, n := range names {
		if err := s.declare(n, files[n], loaded[n]); err != nil {
			return err
		}
	}
	return nil
}

// DeleteProject removes a project; the next pass removes its containers and network.
func (s *Supervisor) DeleteProject(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.projects[name]
	for n, r := range s.records {
		if r.Spec.Project == name {
			found = true
			if err := s.delete(n); err != nil {
				return err
			}
		}
	}
	if !found {
		return ErrNoProject
	}
	if err := s.store.DeleteProject(name); err != nil {
		return err
	}
	delete(s.projects, name)
	return nil
}

// declare stores a project and its specs, and removes the containers of services it
// no longer has. Called with mu held.
func (s *Supervisor) declare(name string, p store.Project, specs []spec.Spec) error {
	if err := s.store.PutProject(name, p); err != nil {
		return err
	}
	s.projects[name] = p
	keep := map[string]bool{}
	for _, sp := range specs {
		if err := s.put(sp); err != nil {
			return err
		}
		keep[sp.Name] = true
	}
	for n, r := range s.records {
		if r.Spec.Project == name && !keep[n] {
			if err := s.delete(n); err != nil {
				return err
			}
		}
	}
	return nil
}

// Projects reports every project, by name, with its services.
func (s *Supervisor) Projects() []ProjectStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := map[string]bool{}
	for n := range s.projects {
		names[n] = true
	}
	for _, r := range s.records {
		names[r.Spec.Project] = true
	}
	out := make([]ProjectStatus, 0, len(names))
	for n := range names {
		out = append(out, s.project(n))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Project reports one project with its Compose file.
func (s *Supervisor) Project(name string) (ProjectStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.project(name)
	declared, ok := s.projects[name]
	if !ok && len(p.Services) == 0 {
		return ProjectStatus{}, ErrNoProject
	}
	p.Compose = string(declared.Compose)
	return p, nil
}

// project is one project's services. Called with mu held.
func (s *Supervisor) project(name string) ProjectStatus {
	p := ProjectStatus{Name: name, Services: []Status{}}
	if declared, ok := s.projects[name]; ok {
		p.ProjectDir = declared.Dir
		if p.ProjectDir == "" {
			p.ProjectDir = compose.Dir(s.opts.Volumes, name)
		}
	}
	for _, r := range s.records {
		if r.Spec.Project == name {
			p.Services = append(p.Services, s.status(r))
		}
	}
	sort.Slice(p.Services, func(i, j int) bool { return p.Services[i].Spec.Name < p.Services[j].Spec.Name })
	return p
}

// delete removes a declaration. Called with mu held.
func (s *Supervisor) delete(name string) error {
	if err := s.store.Delete(name); err != nil {
		return err
	}
	delete(s.records, name)
	delete(s.track, name)
	s.touch(name)
	return nil
}

// Start makes a container desired running and starts it now, its tallies reset.
func (s *Supervisor) Start(name string) error { return s.setDesired(name, store.Running) }

// Restart makes a container desired running, stops it when it runs, and starts it.
func (s *Supervisor) Restart(name string) error {
	if err := s.setDesired(name, store.Running); err != nil {
		return err
	}
	s.mu.Lock()
	s.tr(name).restart = true
	s.touch(name)
	s.mu.Unlock()
	return nil
}

// Subscribe returns a channel of every phase change until cancel is called. A slow
// reader loses changes rather than stalling the supervisor.
func (s *Supervisor) Subscribe() (changes <-chan Change, cancel func()) {
	c := make(chan Change, 64)
	s.subMu.Lock()
	s.subs[c] = struct{}{}
	s.subMu.Unlock()
	return c, func() {
		s.subMu.Lock()
		if _, ok := s.subs[c]; ok {
			delete(s.subs, c)
			close(c)
		}
		s.subMu.Unlock()
	}
}

// publish sends the phase changes of the last pass. Called with mu held.
func (s *Supervisor) publish() {
	var changes []Change
	for name, r := range s.records {
		st := s.status(r)
		if s.lastPhase[name] == st.Phase {
			continue
		}
		s.lastPhase[name] = st.Phase
		changes = append(changes, Change{Name: name, Phase: st.Phase, Desired: st.Desired, ExitCode: st.ExitCode, Time: s.opts.Now()})
	}
	for name := range s.lastPhase {
		if _, ok := s.records[name]; !ok {
			delete(s.lastPhase, name)
			changes = append(changes, Change{Name: name, Phase: "deleted", Time: s.opts.Now()})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for c := range s.subs {
		for _, ch := range changes {
			select {
			case c <- ch:
			default:
			}
		}
	}
}

// Stop makes a container desired stopped; the stop is persisted.
func (s *Supervisor) Stop(name string) error { return s.setDesired(name, store.Stopped) }

func (s *Supervisor) setDesired(name, desired string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[name]
	if !ok {
		return ErrNotFound
	}
	if r.Desired != desired {
		r.Desired = desired
		if err := s.store.Put(r); err != nil {
			return err
		}
		s.records[name] = r
	}
	if desired == store.Running {
		*s.tr(name) = tracking{force: true}
	}
	s.touch(name)
	return nil
}

// Logs reads a declared container's log through the engine.
func (s *Supervisor) Logs(ctx context.Context, name string, tail int, follow bool) (io.ReadCloser, error) {
	s.mu.Lock()
	_, ok := s.records[name]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	return s.engine.Logs(ctx, name, tail, follow)
}

// Get reports one declared container.
func (s *Supervisor) Get(name string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[name]
	if !ok {
		return Status{}, ErrNotFound
	}
	return s.status(r), nil
}

// List reports every declared container, by name.
func (s *Supervisor) List() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, s.status(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
	return out
}

func (s *Supervisor) status(r store.Record) Status {
	st := Status{Project: r.Spec.Project, Service: r.Spec.Service, Spec: r.Spec, Desired: r.Desired, Phase: PhaseStopped}
	if t := s.track[r.Spec.Name]; t != nil {
		if t.phase != "" {
			st.Phase = t.phase
		}
		st.StartAttempts, st.Restarts, st.NextAttempt, st.Error, st.Health = t.startAttempts, t.restarts, t.nextAttempt, t.err, t.health
	}
	if c, ok := s.observed[r.Spec.Name]; ok {
		st.Exists, st.ImageID, st.StartedAt, st.ExitedAt, st.ExitCode = true, c.ImageID, c.StartedAt, c.ExitedAt, c.ExitCode
	}
	return st
}

// The restart timings, as durations.
const (
	startConfirm = spec.StartSeconds * time.Second
	backoffMax   = spec.BackoffMaxSeconds * time.Second
)

// gap is the wait before attempt n: n steps of backoff, at most the cap.
func gap(n int) time.Duration {
	return min(time.Duration(n*spec.BackoffSeconds)*time.Second, backoffMax)
}

// Pass brings every container to its declaration once, and returns when the next
// pass is due for a deadline of its own (a backoff ending, a start to confirm), or
// the zero time. Passes run one at a time; the API is served while one runs.
func (s *Supervisor) Pass(ctx context.Context) (time.Time, error) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	listed, err := s.engine.List(ctx)
	if err != nil {
		return time.Time{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed = map[string]engine.Container{}
	s.foreign = map[string]engine.Container{}
	for _, c := range listed {
		if !c.Managed() {
			s.foreign[c.Name] = c
			continue
		}
		if _, declared := s.records[c.Name]; !declared {
			s.opts.Log.Info("removing a container nothing declares", "name", c.Name)
			s.unlocked(c.Name, func() { err = s.engine.Remove(ctx, c.Name) })
			if err != nil {
				s.opts.Log.Error("remove failed", "name", c.Name, "error", err)
			}
			continue
		}
		s.observed[c.Name] = c
	}
	var next time.Time
	due := func(t time.Time) {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	names := make([]string, 0, len(s.records))
	for n := range s.records {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if r, ok := s.records[name]; ok {
			due(s.reconcile(ctx, r))
		}
	}
	s.removeNetworks(ctx)
	s.publish()
	return next, nil
}

// removeNetworks removes the networks of projects nothing declares any more, once
// their containers are gone. Called with mu held, released around the engine calls.
func (s *Supervisor) removeNetworks(ctx context.Context) {
	declared := map[string]bool{}
	for _, r := range s.records {
		declared[r.Spec.Network] = true
	}
	s.mu.Unlock()
	defer s.mu.Lock()
	nets, err := s.engine.Networks(ctx)
	if err != nil {
		s.opts.Log.Error("listing networks failed", "error", err)
		return
	}
	for _, n := range nets {
		if declared[n.Name] {
			continue
		}
		s.opts.Log.Info("removing the network of a project nothing declares", "network", n.Name, "project", n.Project)
		if err := s.engine.RemoveNetwork(ctx, n.Name); err != nil {
			s.opts.Log.Error("removing a network failed", "network", n.Name, "error", err)
		}
	}
}

// reconcile brings one container to its declaration; it returns its own next deadline.
func (s *Supervisor) reconcile(ctx context.Context, r store.Record) time.Time {
	name, sp, now := r.Spec.Name, r.Spec.Normalized(), s.opts.Now()
	t := s.tr(name)
	c, exists := s.observed[name]

	if exists && c.Labels[engine.LabelSpec] != sp.Hash() {
		s.opts.Log.Info("recreating a container whose declaration changed", "name", name)
		var err error
		if !s.unlocked(name, func() { err = s.engine.Remove(ctx, name) }) {
			return time.Time{}
		}
		if err != nil {
			t.err = err.Error()
			return time.Time{}
		}
		delete(s.observed, name)
		exists = false
		if r.Desired == store.Running && !t.force {
			*t = tracking{force: true}
		}
	}

	if r.Desired == store.Stopped {
		if exists && c.Running() {
			var err error
			if !s.unlocked(name, func() { err = s.engine.Stop(ctx, name, s.opts.StopTimeout) }) {
				return time.Time{}
			}
			if err != nil {
				t.err = err.Error()
				return time.Time{}
			}
		}
		*t = tracking{phase: PhaseStopped}
		if exists {
			t.handledExit = c.ExitedAt
		}
		return time.Time{}
	}

	if exists && c.Running() && t.restart {
		var err error
		if !s.unlocked(name, func() { err = s.engine.Stop(ctx, name, s.opts.StopTimeout) }) {
			return time.Time{}
		}
		if err != nil {
			t.err = err.Error()
			return time.Time{}
		}
		t.restart = false
		return s.start(ctx, t, sp, exists, now)
	}
	t.restart = false
	if exists && c.Running() {
		t.force = false
		t.err = ""
		var next time.Time
		if confirmed := c.StartedAt.Add(startConfirm); now.Before(confirmed) {
			t.phase, next = PhaseStarting, confirmed
		} else {
			t.phase = PhaseRunning
		}
		if sp.Health != nil {
			due, current := s.health(ctx, t, sp, c, now)
			if !current {
				return time.Time{}
			}
			if next.IsZero() || due.Before(next) {
				next = due
			}
		}
		return next
	}
	t.health, t.failures, t.healthRun = "", 0, time.Time{}

	switch {
	case t.force, !exists, c.StartedAt.IsZero(), c.StartedAt.Before(s.opts.BootTime):
		// Started through the API, never created, never started, or not since this
		// boot: a start, not a restart.
		if !t.force && t.phase == PhaseFatal {
			return time.Time{}
		}
		if !t.force && t.phase == PhaseBackoff && now.Before(t.nextAttempt) {
			return t.nextAttempt
		}
		if t.force || t.phase == "" || t.phase == PhaseStopped {
			*t = tracking{}
		}
		return s.start(ctx, t, sp, exists, now)
	case t.phase == PhaseFatal || (t.phase == PhaseExited && c.ExitedAt.Equal(t.handledExit)):
		return time.Time{}
	case t.phase == PhaseWaiting && c.ExitedAt.Equal(t.handledExit):
		return s.start(ctx, t, sp, exists, now)
	case t.phase == PhaseBackoff && c.ExitedAt.Equal(t.handledExit):
		if now.Before(t.nextAttempt) {
			return t.nextAttempt
		}
		return s.start(ctx, t, sp, exists, now)
	}

	// An exit not yet decided on.
	t.handledExit = c.ExitedAt
	ran := c.ExitedAt.Sub(c.StartedAt)
	if ran > backoffMax {
		t.startAttempts, t.restarts = 0, 0
	}
	restart := sp.Restart.Policy == spec.PolicyAlways || (sp.Restart.Policy == spec.PolicyOnFailure && c.ExitCode != 0)
	if !restart {
		t.phase = PhaseExited
		s.opts.Log.Info("container exited; its policy leaves it stopped", "name", name, "exit_code", c.ExitCode)
		return time.Time{}
	}
	var n int
	if ran < startConfirm {
		t.startAttempts++
		n = t.startAttempts
		if t.startAttempts > spec.StartRetries {
			t.phase = PhaseFatal
			s.opts.Log.Error("container never reached running; giving up", "name", name, "attempts", t.startAttempts)
			return time.Time{}
		}
	} else {
		t.restarts++
		n = t.restarts
		if sp.Restart.MaxRestarts > 0 && t.restarts > sp.Restart.MaxRestarts {
			t.phase = PhaseFatal
			s.opts.Log.Error("container keeps stopping; giving up", "name", name, "restarts", t.restarts-1)
			return time.Time{}
		}
	}
	t.phase = PhaseBackoff
	t.nextAttempt = now.Add(gap(n))
	s.opts.Log.Info("container exited; restarting after a gap", "name", name, "exit_code", c.ExitCode, "at", t.nextAttempt)
	if !now.Before(t.nextAttempt) {
		return s.start(ctx, t, sp, exists, now)
	}
	return t.nextAttempt
}

// start runs a container that does not exist and starts one that does, but for one
// with devices: that is run anew, so each device link names the node it has now. A
// failure counts as an attempt that never reached running.
func (s *Supervisor) start(ctx context.Context, t *tracking, sp spec.Spec, exists bool, now time.Time) time.Time {
	if c, held := s.foreign[sp.Name]; held && !exists {
		// Running it would replace a container mica-containerd did not create.
		t.phase, t.err = PhaseWaiting, fmt.Sprintf("the name is held by container %.12s, which mica-containerd does not manage", c.ID)
		return time.Time{}
	}
	if waiting := s.unready(sp); waiting != "" {
		t.phase, t.err = PhaseWaiting, "waiting for "+waiting
		return now.Add(time.Second)
	}
	var err error
	if !s.unlocked(sp.Name, func() {
		switch {
		case exists && len(sp.Devices) == 0:
			err = s.engine.Start(ctx, sp.Name)
		case sp.Network == "": // host or none
			err = s.engine.Run(ctx, sp)
		default:
			if err = s.engine.CreateNetwork(ctx, sp.Network, sp.Project); err == nil {
				err = s.engine.Run(ctx, sp)
			}
		}
	}) {
		return time.Time{}
	}
	t.force = false
	if err != nil {
		t.err = err.Error()
		t.startAttempts++
		if t.startAttempts > spec.StartRetries {
			t.phase = PhaseFatal
			s.opts.Log.Error("container cannot be started; giving up", "name", sp.Name, "error", err)
			return time.Time{}
		}
		t.phase = PhaseBackoff
		t.nextAttempt = now.Add(gap(t.startAttempts))
		s.opts.Log.Error("container start failed; retrying after a gap", "name", sp.Name, "error", err, "at", t.nextAttempt)
		return t.nextAttempt
	}
	t.err = ""
	t.phase = PhaseStarting
	return now.Add(startConfirm)
}

// unready names the first dependency of sp that is not ready, or "".
func (s *Supervisor) unready(sp spec.Spec) string {
	for _, d := range sp.DependsOn {
		if _, ok := s.records[d.Name]; !ok {
			return d.Name + " (not declared)"
		}
		dt := s.track[d.Name]
		if dt == nil || dt.phase != PhaseRunning {
			return d.Name + " (running)"
		}
		if d.Ready == spec.ReadyHealthy && dt.health != HealthHealthy {
			return d.Name + " (healthy)"
		}
	}
	return ""
}

// health runs the health check of a running container when it is due. The first check
// is due when the run is confirmed, then every interval. It returns when the next is
// due, and false when an API call changed the container during the check (then
// nothing is stored).
func (s *Supervisor) health(ctx context.Context, t *tracking, sp spec.Spec, c engine.Container, now time.Time) (time.Time, bool) {
	h := sp.Health
	if !t.healthRun.Equal(c.StartedAt) {
		t.healthRun, t.health, t.failures = c.StartedAt, HealthStarting, 0
		t.nextHealth = c.StartedAt.Add(startConfirm)
	}
	if now.Before(t.nextHealth) {
		return t.nextHealth, true
	}
	check, cancel := context.WithTimeout(ctx, time.Duration(h.TimeoutSeconds)*time.Second+5*time.Second)
	defer cancel()
	var ok bool
	var err error
	if !s.unlocked(sp.Name, func() { ok, err = s.engine.HealthCheck(check, sp.Name) }) {
		return time.Time{}, false
	}
	t.nextHealth = now.Add(time.Duration(h.IntervalSeconds) * time.Second)
	if err != nil {
		t.err = "health check: " + err.Error()
	}
	if ok {
		t.health, t.failures = HealthHealthy, 0
		return t.nextHealth, true
	}
	if now.Sub(c.StartedAt) < time.Duration(h.StartPeriodSeconds)*time.Second {
		return t.nextHealth, true
	}
	t.failures++
	if t.failures < h.Retries || t.health == HealthUnhealthy {
		return t.nextHealth, true
	}
	t.health = HealthUnhealthy
	s.opts.Log.Warn("container is unhealthy", "name", sp.Name, "failures", t.failures)
	return t.nextHealth, true
}

// Run passes until ctx ends: at once, on a declaration, on a container event, at the
// deadline the last pass returned, and at least every Resync.
func (s *Supervisor) Run(ctx context.Context) error {
	events, errs := s.engine.Events(ctx)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		next, err := s.Pass(ctx)
		if err != nil {
			s.opts.Log.Error("pass failed", "error", err)
		}
		wait := s.opts.Resync
		if !next.IsZero() {
			if d := time.Until(next); d < wait {
				wait = max(d, 100*time.Millisecond)
			}
		}
		timer.Reset(wait) // Go 1.23 timers: a reset never delivers a stale tick
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.wake:
		case <-timer.C:
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case err, ok := <-errs:
			if ok {
				s.opts.Log.Error("the event stream broke; restarting it", "error", err)
			}
			events, errs = s.restartEvents(ctx)
		}
	}
}

func (s *Supervisor) restartEvents(ctx context.Context) (<-chan engine.Event, <-chan error) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	return s.engine.Events(ctx)
}

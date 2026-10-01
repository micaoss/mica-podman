// Package supervisor keeps every declared container in the state it was declared in.
//
// What it decides from is persisted or observed, never memory alone: the desired state
// is in the store, and whether a stopped container is restarted follows from its
// restart policy and the exit code and times podman reports. So a restart of the
// supervisor neither restarts a container someone stopped nor forgets one that died
// while it was down. Events trigger a pass, and a periodic full pass repairs anything
// an event missed.
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

	"github.com/micaoss/mica-podman/mica-containerd/internal/engine"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/store"
)

// Phase is what the supervisor observes of a declared container.
type Phase string

// The phases.
const (
	PhaseStopped  Phase = "stopped"  // desired stopped, or never started
	PhaseStarting Phase = "starting" // started, not yet running for start_seconds
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

// Options configure a Supervisor.
type Options struct {
	// BootID names this boot (/proc/sys/kernel/random/boot_id); a new one brings every
	// container to its boot state: running when autostart, stopped when not.
	BootID string
	// BootTime is when this boot began; a container not started since is started, not
	// judged by its restart policy.
	BootTime time.Time
	// Resync is the period of the full pass.
	Resync time.Duration
	// StopTimeout is how long a stopping container gets before it is killed.
	StopTimeout time.Duration
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
type Supervisor struct {
	store  *store.Store
	engine engine.Engine
	opts   Options

	mu       sync.Mutex
	records  map[string]store.Record
	track    map[string]*tracking
	observed map[string]engine.Container
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
	records, bad, err := st.Load()
	if err != nil {
		return nil, err
	}
	for _, e := range bad {
		opts.Log.Error("a stored declaration is not loaded; it stays on disk", "error", e)
	}
	s := &Supervisor{store: st, engine: en, opts: opts, records: records,
		track: map[string]*tracking{}, observed: map[string]engine.Container{}, wake: make(chan struct{}, 1),
		subs: map[chan Change]struct{}{}, lastPhase: map[string]Phase{}}
	last, err := st.BootID()
	if err != nil {
		return nil, err
	}
	if opts.BootID != "" && last != opts.BootID {
		for name, r := range records {
			r.Desired = store.Stopped
			if r.Spec.Autostart {
				r.Desired = store.Running
			}
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

func (s *Supervisor) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ErrNotFound is a name no declaration carries.
var ErrNotFound = errors.New("no such container")

// Put declares one container, or replaces its declaration. A new one is desired
// running when it starts at boot; a replaced one keeps its desired state.
func (s *Supervisor) Put(sp spec.Spec) error {
	if err := sp.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deps := map[string][]string{}
	for n, rec := range s.records {
		deps[n] = depNames(rec.Spec)
	}
	deps[sp.Name] = depNames(sp)
	if name := cyclic(deps); name != "" {
		return &spec.Error{Rule: "depends_on", Detail: fmt.Sprintf("%q is part of a dependency cycle", name)}
	}
	r, ok := s.records[sp.Name]
	if !ok {
		r.Desired = store.Stopped
		if sp.Autostart {
			r.Desired = store.Running
		}
	}
	r.Spec = sp
	if err := s.store.Put(r); err != nil {
		return err
	}
	s.records[sp.Name] = r
	if t := s.track[sp.Name]; t != nil && t.phase == PhaseFatal {
		*t = tracking{force: true}
	}
	s.poke()
	return nil
}

// PutAll declares exactly specs: every other declaration is removed.
func (s *Supervisor) PutAll(specs []spec.Spec) error {
	names := map[string]bool{}
	deps := map[string][]string{}
	for _, sp := range specs {
		deps[sp.Name] = depNames(sp)
	}
	if name := cyclic(deps); name != "" {
		return &spec.Error{Rule: "depends_on", Detail: fmt.Sprintf("%q is part of a dependency cycle", name)}
	}
	for _, sp := range specs {
		if err := sp.Validate(); err != nil {
			return err
		}
		if names[sp.Name] {
			return &spec.Error{Rule: "name", Detail: fmt.Sprintf("%q is declared twice", sp.Name)}
		}
		names[sp.Name] = true
	}
	for _, sp := range specs {
		if err := s.Put(sp); err != nil {
			return err
		}
	}
	for _, name := range s.Names() {
		if !names[name] {
			if err := s.Delete(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// Delete removes a declaration; the next pass removes its container.
func (s *Supervisor) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[name]; !ok {
		return ErrNotFound
	}
	if err := s.store.Delete(name); err != nil {
		return err
	}
	delete(s.records, name)
	delete(s.track, name)
	s.poke()
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
	s.poke()
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

// Names are the declared containers, sorted.
func (s *Supervisor) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.records))
	for n := range s.records {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
	st := Status{Spec: r.Spec, Desired: r.Desired, Phase: PhaseStopped}
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

// gap is the wait before attempt n: n steps of backoff, at most the cap.
func gap(r spec.Restart, n int) time.Duration {
	d := time.Duration(n*r.BackoffSeconds) * time.Second
	if max := time.Duration(r.BackoffMaxSeconds) * time.Second; d > max {
		return max
	}
	return d
}

// Pass brings every container to its declaration once, and returns when the next
// pass is due for a deadline of its own (a backoff ending, a start to confirm), or
// the zero time.
func (s *Supervisor) Pass(ctx context.Context) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	listed, err := s.engine.List(ctx)
	if err != nil {
		return time.Time{}, err
	}
	s.observed = map[string]engine.Container{}
	for _, c := range listed {
		if _, declared := s.records[c.Name]; !declared {
			s.opts.Log.Info("removing a container nothing declares", "name", c.Name)
			if err := s.engine.Remove(ctx, c.Name); err != nil {
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
		due(s.reconcile(ctx, s.records[name]))
	}
	s.publish()
	return next, nil
}

// reconcile brings one container to its declaration; it returns its own next deadline.
func (s *Supervisor) reconcile(ctx context.Context, r store.Record) time.Time {
	name, sp, now := r.Spec.Name, r.Spec.Normalized(), s.opts.Now()
	t := s.tr(name)
	c, exists := s.observed[name]

	if exists && c.Labels[engine.LabelSpec] != sp.Hash() {
		s.opts.Log.Info("recreating a container whose declaration changed", "name", name)
		if err := s.engine.Remove(ctx, name); err != nil {
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
			if err := s.engine.Stop(ctx, name, s.opts.StopTimeout); err != nil {
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
		if err := s.engine.Stop(ctx, name, s.opts.StopTimeout); err != nil {
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
		if confirmed := c.StartedAt.Add(time.Duration(sp.Restart.StartSeconds) * time.Second); now.Before(confirmed) {
			t.phase, next = PhaseStarting, confirmed
		} else {
			t.phase = PhaseRunning
		}
		if sp.Health != nil {
			due, unhealthy := s.health(ctx, t, sp, c, now)
			if unhealthy && sp.Restart.Policy == spec.PolicyOnUnhealthy {
				s.opts.Log.Warn("container is unhealthy; stopping it to restart it", "name", name)
				if err := s.engine.Stop(ctx, name, s.opts.StopTimeout); err != nil {
					t.err = err.Error()
				}
				return now // the next pass decides on the exit
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
	if ran > time.Duration(sp.Restart.BackoffMaxSeconds)*time.Second {
		t.startAttempts, t.restarts = 0, 0
	}
	restart := sp.Restart.Policy == spec.PolicyAlways ||
		((sp.Restart.Policy == spec.PolicyOnFailure || sp.Restart.Policy == spec.PolicyOnUnhealthy) && c.ExitCode != 0)
	if !restart {
		t.phase = PhaseExited
		s.opts.Log.Info("container exited; its policy leaves it stopped", "name", name, "exit_code", c.ExitCode)
		return time.Time{}
	}
	var n int
	if ran < time.Duration(sp.Restart.StartSeconds)*time.Second {
		t.startAttempts++
		n = t.startAttempts
		if t.startAttempts > sp.Restart.StartRetries {
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
	t.nextAttempt = now.Add(gap(sp.Restart, n))
	s.opts.Log.Info("container exited; restarting after a gap", "name", name, "exit_code", c.ExitCode, "at", t.nextAttempt)
	if !now.Before(t.nextAttempt) {
		return s.start(ctx, t, sp, exists, now)
	}
	return t.nextAttempt
}

// start runs a container that does not exist and starts one that does. A failure
// counts as an attempt that never reached running.
func (s *Supervisor) start(ctx context.Context, t *tracking, sp spec.Spec, exists bool, now time.Time) time.Time {
	if waiting := s.unready(sp); waiting != "" {
		t.phase, t.err = PhaseWaiting, "waiting for "+waiting
		return now.Add(time.Second)
	}
	var err error
	if exists {
		err = s.engine.Start(ctx, sp.Name)
	} else {
		err = s.engine.Run(ctx, sp)
	}
	t.force = false
	if err != nil {
		t.err = err.Error()
		t.startAttempts++
		if t.startAttempts > sp.Restart.StartRetries {
			t.phase = PhaseFatal
			s.opts.Log.Error("container cannot be started; giving up", "name", sp.Name, "error", err)
			return time.Time{}
		}
		t.phase = PhaseBackoff
		t.nextAttempt = now.Add(gap(sp.Restart, t.startAttempts))
		s.opts.Log.Error("container start failed; retrying after a gap", "name", sp.Name, "error", err, "at", t.nextAttempt)
		return t.nextAttempt
	}
	t.err = ""
	t.phase = PhaseStarting
	return now.Add(time.Duration(sp.Restart.StartSeconds) * time.Second)
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
// is due when the run is confirmed (start_seconds), then every interval. It returns
// when the next is due, and whether the container just became unhealthy.
func (s *Supervisor) health(ctx context.Context, t *tracking, sp spec.Spec, c engine.Container, now time.Time) (time.Time, bool) {
	h := sp.Health
	if !t.healthRun.Equal(c.StartedAt) {
		t.healthRun, t.health, t.failures = c.StartedAt, HealthStarting, 0
		t.nextHealth = c.StartedAt.Add(time.Duration(sp.Restart.StartSeconds) * time.Second)
	}
	if now.Before(t.nextHealth) {
		return t.nextHealth, false
	}
	check, cancel := context.WithTimeout(ctx, time.Duration(h.TimeoutSeconds)*time.Second+5*time.Second)
	defer cancel()
	ok, err := s.engine.HealthCheck(check, sp.Name)
	t.nextHealth = now.Add(time.Duration(h.IntervalSeconds) * time.Second)
	if err != nil {
		t.err = "health check: " + err.Error()
	}
	if ok {
		t.health, t.failures = HealthHealthy, 0
		return t.nextHealth, false
	}
	if now.Sub(c.StartedAt) < time.Duration(h.StartPeriodSeconds)*time.Second {
		return t.nextHealth, false
	}
	t.failures++
	if t.failures < h.Retries || t.health == HealthUnhealthy {
		return t.nextHealth, false
	}
	t.health = HealthUnhealthy
	s.opts.Log.Warn("container is unhealthy", "name", sp.Name, "failures", t.failures)
	return t.nextHealth, true
}

func depNames(sp spec.Spec) []string {
	names := make([]string, len(sp.DependsOn))
	for i, d := range sp.DependsOn {
		names[i] = d.Name
	}
	return names
}

// cyclic names a container on a dependency cycle of deps, or "".
func cyclic(deps map[string][]string) string {
	const (
		unseen = iota
		open
		done
	)
	state := map[string]int{}
	var visit func(string) string
	visit = func(n string) string {
		switch state[n] {
		case open:
			return n
		case done:
			return ""
		}
		state[n] = open
		for _, d := range deps[n] {
			if c := visit(d); c != "" {
				return c
			}
		}
		state[n] = done
		return ""
	}
	names := make([]string, 0, len(deps))
	for n := range deps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if c := visit(n); c != "" {
			return c
		}
	}
	return ""
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

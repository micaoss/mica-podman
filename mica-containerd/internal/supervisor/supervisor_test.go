package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/engine"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/store"
)

// clock is the test's time; it moves only when told.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) add(d time.Duration)     { c.t = c.t.Add(d) }
func (c *clock) addSeconds(n int)        { c.add(time.Duration(n) * time.Second) }
func newClock(boot time.Time) *clock     { return &clock{t: boot.Add(time.Minute)} }
func quiet() *slog.Logger                { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func bootAt() time.Time                  { return time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC) }
func ctx() context.Context               { return context.Background() }
func secs(n int) time.Duration           { return time.Duration(n) * time.Second }
func fixed(t time.Time) func() time.Time { return func() time.Time { return t } }

// fake is an engine whose containers change only when the supervisor or the test acts.
type fake struct {
	mu         sync.Mutex
	clock      *clock
	containers map[string]*engine.Container
	calls      []string
	failRun    error
	unhealthy  map[string]bool
}

func newFake(c *clock) *fake {
	return &fake{clock: c, containers: map[string]*engine.Container{}, unhealthy: map[string]bool{}}
}

func (f *fake) HealthCheck(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("health " + name)
	return !f.unhealthy[name], nil
}

func (f *fake) record(call string) { f.calls = append(f.calls, call) }

func (f *fake) List(context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []engine.Container{}
	for _, c := range f.containers {
		out = append(out, *c)
	}
	return out, nil
}

func (f *fake) Run(_ context.Context, s spec.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("run " + s.Name)
	if f.failRun != nil {
		return f.failRun
	}
	f.containers[s.Name] = &engine.Container{Name: s.Name, State: "running", StartedAt: f.clock.now(),
		Labels: map[string]string{engine.LabelManaged: "1", engine.LabelSpec: s.Hash()}}
	return nil
}

func (f *fake) Start(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("start " + name)
	c := f.containers[name]
	c.State, c.StartedAt, c.ExitedAt, c.ExitCode = "running", f.clock.now(), time.Time{}, 0
	return nil
}

func (f *fake) Stop(_ context.Context, name string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("stop " + name)
	c := f.containers[name]
	c.State, c.ExitedAt, c.ExitCode = "exited", f.clock.now(), 143
	return nil
}

func (f *fake) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("remove " + name)
	delete(f.containers, name)
	return nil
}

func (f *fake) Logs(_ context.Context, name string, _ int, _ bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("log of " + name + "\n")), nil
}

func (f *fake) Events(context.Context) (<-chan engine.Event, <-chan error) {
	return make(chan engine.Event), make(chan error)
}

// exit makes a container stop on its own with code.
func (f *fake) exit(name string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[name]
	c.State, c.ExitedAt, c.ExitCode = "exited", f.clock.now(), code
}

func (f *fake) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

type rig struct {
	t     *testing.T
	clock *clock
	en    *fake
	st    *store.Store
	sv    *Supervisor
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newClock(bootAt())
	r := &rig{t: t, clock: c, en: newFake(c), st: st}
	r.restart("boot-1")
	return r
}

// restart brings the supervisor up again on the same store and engine: a restart of
// the daemon when bootID is the same, a reboot when it is not.
func (r *rig) restart(bootID string) {
	r.t.Helper()
	sv, err := New(r.st, r.en, Options{BootID: bootID, BootTime: bootAt(), Now: r.clock.now, Log: quiet()})
	if err != nil {
		r.t.Fatal(err)
	}
	r.sv = sv
}

func (r *rig) pass() {
	r.t.Helper()
	if _, err := r.sv.Pass(ctx()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) phase(name string) Phase {
	r.t.Helper()
	st, err := r.sv.Get(name)
	if err != nil {
		r.t.Fatal(err)
	}
	return st.Phase
}

func (r *rig) want(name string, p Phase) {
	r.t.Helper()
	if got := r.phase(name); got != p {
		r.t.Fatalf("%s is %s, want %s (calls %v)", name, got, p, r.en.calls)
	}
}

func (r *rig) put(s spec.Spec) {
	r.t.Helper()
	if err := r.sv.Put(s); err != nil {
		r.t.Fatal(err)
	}
}

func web(policy string) spec.Spec {
	return spec.Spec{Name: "web", Image: "docker.io/library/nginx:1", Autostart: true, Restart: spec.Restart{Policy: policy}}
}

// runs brings a started container past start_seconds.
func (r *rig) runs(name string) {
	r.t.Helper()
	r.clock.addSeconds(spec.DefaultStartSeconds)
	r.pass()
	r.want(name, PhaseRunning)
}

func TestADeclaredAutostartContainerRunsAndIsConfirmed(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyNo))
	r.pass()
	r.want("web", PhaseStarting)
	r.runs("web")
	if r.en.count("run web") != 1 {
		t.Fatalf("calls %v", r.en.calls)
	}
}

func TestANonAutostartContainerWaitsForAStart(t *testing.T) {
	r := newRig(t)
	s := web(spec.PolicyNo)
	s.Autostart = false
	r.put(s)
	r.pass()
	r.want("web", PhaseStopped)
	if err := r.sv.Start("web"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	r.want("web", PhaseStarting)
}

func TestAlwaysRestartsAfterAGap(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyAlways))
	r.pass()
	r.runs("web")
	r.clock.addSeconds(10)
	r.en.exit("web", 0)
	r.pass()
	r.want("web", PhaseBackoff)
	if r.en.count("start web") != 0 {
		t.Fatal("restarted before the gap")
	}
	r.clock.addSeconds(spec.DefaultBackoffSeconds)
	r.pass()
	r.want("web", PhaseStarting)
	if st, _ := r.sv.Get("web"); st.Restarts != 1 || r.en.count("start web") != 1 {
		t.Fatalf("restarts %d, calls %v", st.Restarts, r.en.calls)
	}
}

func TestOnFailureRestartsOnlyANonZeroExit(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyOnFailure))
	r.pass()
	r.runs("web")
	r.clock.addSeconds(10)
	r.en.exit("web", 1)
	r.pass()
	r.want("web", PhaseBackoff)
	r.clock.addSeconds(5)
	r.pass()
	r.runs("web")
	r.clock.addSeconds(10)
	r.en.exit("web", 0)
	r.pass()
	r.want("web", PhaseExited)
	r.clock.addSeconds(120)
	r.pass()
	r.want("web", PhaseExited)
}

func TestNoLeavesItStoppedUntilAStart(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyNo))
	r.pass()
	r.runs("web")
	r.en.exit("web", 1)
	r.pass()
	r.want("web", PhaseExited)
	if err := r.sv.Start("web"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	r.want("web", PhaseStarting)
	if r.en.count("start web") != 1 {
		t.Fatalf("calls %v", r.en.calls)
	}
}

func TestStartRetriesEndInFatal(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyAlways))
	r.pass() // run
	for i := 1; i <= spec.DefaultStartRetries; i++ {
		r.en.exit("web", 1) // dies before start_seconds
		r.pass()
		r.want("web", PhaseBackoff)
		r.clock.add(gap(web(spec.PolicyAlways).Normalized().Restart, i))
		r.pass()
		r.want("web", PhaseStarting)
	}
	r.en.exit("web", 1)
	r.pass()
	r.want("web", PhaseFatal)
	starts := r.en.count("start web")
	r.clock.addSeconds(600)
	r.pass()
	if r.en.count("start web") != starts {
		t.Fatal("a fatal container was started again")
	}
	if err := r.sv.Start("web"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	r.want("web", PhaseStarting)
}

func TestMaxRestartsEndInFatal(t *testing.T) {
	r := newRig(t)
	s := web(spec.PolicyAlways)
	s.Restart.MaxRestarts = 2
	s.Restart.BackoffMaxSeconds = 600
	r.put(s)
	r.pass()
	for i := 1; i <= 2; i++ {
		r.runs("web")
		r.clock.addSeconds(10)
		r.en.exit("web", 0)
		r.pass()
		r.want("web", PhaseBackoff)
		r.clock.addSeconds(i)
		r.pass()
	}
	r.runs("web")
	r.clock.addSeconds(10)
	r.en.exit("web", 0)
	r.pass()
	r.want("web", PhaseFatal)
}

func TestBackoffStepsUpToItsCap(t *testing.T) {
	rs := spec.Restart{BackoffSeconds: 2, BackoffMaxSeconds: 5}
	for n, want := range map[int]time.Duration{1: secs(2), 2: secs(4), 3: secs(5), 9: secs(5)} {
		if got := gap(rs, n); got != want {
			t.Errorf("gap %d = %s, want %s", n, got, want)
		}
	}
}

func TestARunLongerThanTheCapResetsTheTallies(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyAlways))
	r.pass()
	r.runs("web")
	r.clock.addSeconds(10)
	r.en.exit("web", 0)
	r.pass()
	r.clock.addSeconds(1)
	r.pass()
	r.runs("web")
	r.clock.addSeconds(spec.DefaultBackoffMaxSeconds + 1)
	r.en.exit("web", 0)
	r.pass()
	if st, _ := r.sv.Get("web"); st.Restarts != 1 {
		t.Fatalf("restarts %d after a long run, want 1", st.Restarts)
	}
}

func TestAStopSurvivesARestartOfTheSupervisor(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyAlways))
	r.pass()
	r.runs("web")
	if err := r.sv.Stop("web"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	r.want("web", PhaseStopped)
	r.restart("boot-1")
	r.clock.addSeconds(120)
	r.pass()
	r.want("web", PhaseStopped)
	if r.en.count("start web") != 0 {
		t.Fatalf("a stopped container was started after a restart of the supervisor: %v", r.en.calls)
	}
}

func TestAnExitWhileTheSupervisorWasDownIsDecidedFromFacts(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyOnFailure))
	r.pass()
	r.runs("web")
	r.clock.addSeconds(30)
	r.en.exit("web", 2)
	r.restart("boot-1")
	r.pass()
	r.want("web", PhaseBackoff)
	r.clock.addSeconds(1)
	r.pass()
	r.want("web", PhaseStarting)
}

func TestARebootStartsAutostartContainersWhateverTheirPolicy(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyNo))
	manual := spec.Spec{Name: "manual", Image: "i"}
	r.put(manual)
	if err := r.sv.Start("manual"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	// The previous boot: both ran and stopped before this boot began.
	for _, n := range []string{"web", "manual"} {
		c := r.en.containers[n]
		c.State, c.StartedAt, c.ExitedAt = "exited", bootAt().Add(-time.Hour), bootAt().Add(-time.Minute)
	}
	r.restart("boot-2")
	r.pass()
	r.want("web", PhaseStarting)
	r.want("manual", PhaseStopped)
	if got, _ := r.sv.Get("manual"); got.Desired != store.Stopped {
		t.Fatalf("a non-autostart container is desired %s after a reboot", got.Desired)
	}
}

func TestADeclarationChangeRecreatesOnlyForTheRuntimePart(t *testing.T) {
	r := newRig(t)
	s := web(spec.PolicyNo)
	r.put(s)
	r.pass()
	r.runs("web")
	s.Restart.Policy = spec.PolicyAlways
	r.put(s)
	r.pass()
	if r.en.count("remove web") != 0 {
		t.Fatal("a restart-policy change recreated the container")
	}
	s.Environment = map[string]string{"A": "1"}
	r.put(s)
	r.pass()
	if r.en.count("remove web") != 1 || r.en.count("run web") != 2 {
		t.Fatalf("calls %v", r.en.calls)
	}
	if r.en.containers["web"].Labels[engine.LabelSpec] != s.Hash() {
		t.Fatal("the new container does not carry the new hash")
	}
}

func TestUndeclaredAndDeletedContainersAreRemoved(t *testing.T) {
	r := newRig(t)
	r.en.containers["stray"] = &engine.Container{Name: "stray", State: "running", Labels: map[string]string{engine.LabelManaged: "1"}}
	r.put(web(spec.PolicyNo))
	r.pass()
	if _, ok := r.en.containers["stray"]; ok {
		t.Fatal("an undeclared managed container was left")
	}
	if err := r.sv.Delete("web"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	if _, ok := r.en.containers["web"]; ok {
		t.Fatal("a deleted declaration left its container")
	}
	if err := r.sv.Delete("web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
}

func TestARunThatFailsBacksOffThenGivesUp(t *testing.T) {
	r := newRig(t)
	r.en.failRun = errors.New("pull denied")
	r.put(web(spec.PolicyNo))
	r.pass()
	r.want("web", PhaseBackoff)
	if st, _ := r.sv.Get("web"); st.Error == "" {
		t.Fatal("the failure is not reported")
	}
	for i := 1; i <= spec.DefaultStartRetries; i++ {
		r.clock.addSeconds(spec.DefaultBackoffMaxSeconds)
		r.pass()
	}
	r.want("web", PhaseFatal)
	runs := r.en.count("run web")
	r.clock.addSeconds(600)
	r.pass()
	if r.en.count("run web") != runs {
		t.Fatal("a fatal container was run again")
	}
	r.en.failRun = nil
	if err := r.sv.Start("web"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	r.want("web", PhaseStarting)
}

func TestStopStopsARunningContainerOnce(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyAlways))
	r.pass()
	r.runs("web")
	_ = r.sv.Stop("web")
	r.pass()
	r.pass()
	if r.en.count("stop web") != 1 {
		t.Fatalf("calls %v", r.en.calls)
	}
	r.want("web", PhaseStopped)
}

func TestPutAllDeclaresExactlyTheSet(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyNo))
	if err := r.sv.PutAll([]spec.Spec{{Name: "a", Image: "i"}, {Name: "b", Image: "i"}}); err != nil {
		t.Fatal(err)
	}
	if got := r.sv.Names(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("names %v", got)
	}
	var e *spec.Error
	if err := r.sv.PutAll([]spec.Spec{{Name: "a", Image: "i"}, {Name: "a", Image: "j"}}); !errors.As(err, &e) {
		t.Fatalf("a name twice: %v", err)
	}
	if got := r.sv.Names(); len(got) != 2 {
		t.Fatal("a refused set changed the declarations")
	}
}

func TestPassReturnsTheNextDeadline(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyAlways))
	next, _ := r.sv.Pass(ctx())
	if want := r.clock.now().Add(secs(spec.DefaultStartSeconds)); !next.Equal(want) {
		t.Fatalf("next %s, want the confirmation at %s", next, want)
	}
	r.runs("web")
	r.en.exit("web", 0)
	next, _ = r.sv.Pass(ctx())
	if !next.After(r.clock.now()) {
		t.Fatalf("next %s is not the backoff's end", next)
	}
}

func TestRunPassesOnAWakeAndStops(t *testing.T) {
	r := newRig(t)
	sv, err := New(r.st, r.en, Options{BootID: "boot-1", BootTime: bootAt(), Now: fixed(r.clock.now()), Log: quiet(), Resync: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx())
	done := make(chan error, 1)
	go func() { done <- sv.Run(c) }()
	if err := sv.Put(web(spec.PolicyNo)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.en.count("run web") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run ended with %v", err)
	}
	if r.en.count("run web") != 1 {
		t.Fatal("a declaration did not wake the loop")
	}
}

func TestRestartStopsAndStartsARunningContainer(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyNo))
	r.pass()
	r.runs("web")
	if err := r.sv.Restart("web"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	if r.en.count("stop web") != 1 || r.en.count("start web") != 1 {
		t.Fatalf("calls %v", r.en.calls)
	}
	r.want("web", PhaseStarting)
	r.pass()
	if r.en.count("stop web") != 1 {
		t.Fatal("a restart was done twice")
	}
}

func TestSubscribersSeePhaseChanges(t *testing.T) {
	r := newRig(t)
	changes, cancel := r.sv.Subscribe()
	defer cancel()
	r.put(web(spec.PolicyNo))
	r.pass()
	if c := <-changes; c.Name != "web" || c.Phase != PhaseStarting {
		t.Fatalf("first change %+v", c)
	}
	r.pass()
	select {
	case c := <-changes:
		t.Fatalf("a pass without a change published %+v", c)
	default:
	}
	_ = r.sv.Delete("web")
	r.pass()
	if c := <-changes; c.Phase != "deleted" {
		t.Fatalf("deletion published %+v", c)
	}
	cancel()
	if _, ok := <-changes; ok {
		t.Fatal("the channel is open after cancel")
	}
	cancel()
}

func checked(policy string) spec.Spec {
	s := web(policy)
	s.Health = &spec.Health{Command: []string{"true"}, IntervalSeconds: 10, TimeoutSeconds: 5, Retries: 2}
	return s
}

func TestAHealthCheckRunsOnTheInterval(t *testing.T) {
	r := newRig(t)
	r.put(checked(spec.PolicyNo))
	r.pass()
	r.runs("web") // the first check is due when the run is confirmed
	if st, _ := r.sv.Get("web"); st.Health != HealthHealthy || r.en.count("health web") != 1 {
		t.Fatalf("health %q, calls %v", st.Health, r.en.calls)
	}
	r.clock.addSeconds(5)
	r.pass()
	if r.en.count("health web") != 1 {
		t.Fatal("checked before the interval")
	}
	r.clock.addSeconds(5)
	r.pass()
	if r.en.count("health web") != 2 {
		t.Fatal("not checked at the interval")
	}
}

func TestOnUnhealthyRestartsAfterRetriesFailures(t *testing.T) {
	r := newRig(t)
	r.put(checked(spec.PolicyOnUnhealthy))
	r.pass()
	r.runs("web")
	r.en.unhealthy["web"] = true
	r.clock.addSeconds(10)
	r.pass() // failure 1
	if r.en.count("stop web") != 0 {
		t.Fatal("stopped after one failure of two")
	}
	r.clock.addSeconds(10)
	r.pass() // failure 2: unhealthy, stopped
	if r.en.count("stop web") != 1 {
		t.Fatalf("calls %v", r.en.calls)
	}
	r.pass() // the exit (143) is decided: a restart after a gap
	r.want("web", PhaseBackoff)
	r.en.unhealthy["web"] = false
	r.clock.addSeconds(1)
	r.pass()
	r.want("web", PhaseStarting)
}

func TestFailuresInTheStartPeriodDoNotCount(t *testing.T) {
	r := newRig(t)
	s := checked(spec.PolicyOnUnhealthy)
	s.Health.StartPeriodSeconds = 60
	r.put(s)
	r.en.unhealthy["web"] = true
	r.pass()
	r.runs("web")
	for i := 0; i < 4; i++ {
		r.clock.addSeconds(10)
		r.pass()
	}
	if st, _ := r.sv.Get("web"); st.Health != HealthStarting || r.en.count("stop web") != 0 {
		t.Fatalf("health %q, calls %v", st.Health, r.en.calls)
	}
}

func TestAnotherPolicyLeavesAnUnhealthyContainerRunning(t *testing.T) {
	r := newRig(t)
	r.put(checked(spec.PolicyAlways))
	r.en.unhealthy["web"] = true
	r.pass()
	r.runs("web")
	r.clock.addSeconds(10)
	r.pass()
	if st, _ := r.sv.Get("web"); st.Health != HealthUnhealthy || r.en.count("stop web") != 0 {
		t.Fatalf("health %q, calls %v", st.Health, r.en.calls)
	}
}

func TestAContainerWaitsForItsDependencies(t *testing.T) {
	r := newRig(t)
	app := spec.Spec{Name: "app", Image: "i", Autostart: true, DependsOn: []spec.Dependency{{Name: "db", Ready: spec.ReadyHealthy}}}
	db := spec.Spec{Name: "db", Image: "i", Autostart: false, Health: &spec.Health{Command: []string{"true"}}}
	r.put(app)
	r.put(db)
	r.pass()
	r.want("app", PhaseWaiting)
	_ = r.sv.Start("db")
	r.pass()
	r.want("app", PhaseWaiting) // db is starting, not healthy
	r.clock.addSeconds(1)
	r.pass() // db confirmed and healthy
	r.pass()
	r.want("app", PhaseStarting)
	if st, _ := r.sv.Get("app"); st.StartAttempts != 0 {
		t.Fatal("waiting counted as start attempts")
	}
}

func TestADependencyCycleIsRefused(t *testing.T) {
	r := newRig(t)
	r.put(spec.Spec{Name: "a", Image: "i", DependsOn: []spec.Dependency{{Name: "b"}}})
	var e *spec.Error
	if err := r.sv.Put(spec.Spec{Name: "b", Image: "i", DependsOn: []spec.Dependency{{Name: "a"}}}); !errors.As(err, &e) || e.Rule != "depends_on" {
		t.Fatalf("a cycle through Put: %v", err)
	}
	err := r.sv.PutAll([]spec.Spec{
		{Name: "x", Image: "i", DependsOn: []spec.Dependency{{Name: "y"}}},
		{Name: "y", Image: "i", DependsOn: []spec.Dependency{{Name: "x"}}},
	})
	if !errors.As(err, &e) || e.Rule != "depends_on" {
		t.Fatalf("a cycle through PutAll: %v", err)
	}
	if got := r.sv.Names(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("a refused set changed the declarations: %v", got)
	}
}

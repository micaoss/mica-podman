package supervisor

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/compose"
	"github.com/micaoss/mica-podman/mica-containerd/internal/engine"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
	"github.com/micaoss/mica-podman/mica-containerd/internal/store"
)

const webProject = `services:
  db:
    image: docker.io/library/postgres:17
    restart: unless-stopped
    volumes: [data:/var/lib/postgresql/data]
    healthcheck: {test: [CMD, pg_isready], interval: 5s, timeout: 2s}
  app:
    image: docker.io/library/nginx:1
    restart: always
    ports: ["8080:80"]
    depends_on: {db: {condition: service_healthy}}
volumes:
  data: {}
`

func (r *rig) project(name, file string) {
	r.t.Helper()
	if err := r.sv.PutProject(ctx(), name, store.Project{Compose: []byte(file)}); err != nil {
		r.t.Fatal(err)
	}
}

// hand adds a container a person ran, without the managed label.
func (f *fake) hand(name string) *engine.Container {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &engine.Container{ID: "0123456789ab" + strings.Repeat("0", 52), Name: name, State: "running", Labels: map[string]string{}, StartedAt: f.clock.now()}
	f.containers[name] = c
	return c
}

func TestAProjectRunsItsServicesOnItsNetworkInOrder(t *testing.T) {
	r := newRig(t)
	r.project("web", webProject)
	r.pass()
	if r.en.count("network create web_default") != 1 || r.en.count("run web-db-1") != 1 || r.en.count("run web-app-1") != 0 {
		t.Fatalf("calls %v", r.en.calls)
	}
	r.want("web-app-1", PhaseWaiting)
	r.runs("web-db-1")
	r.clock.addSeconds(5)
	r.pass() // the health check passes
	r.pass()
	if r.en.count("run web-app-1") != 1 {
		t.Fatalf("the app did not start once the db was healthy: %v", r.en.calls)
	}
	st, _ := r.sv.Get("web-app-1")
	if st.Project != "web" || st.Service != "app" || st.Spec.Network != "web_default" || st.Spec.Aliases[0] != "app" {
		t.Fatalf("app: %+v", st)
	}
	db, _ := r.sv.Get("web-db-1")
	if v := db.Spec.Volumes[0]; v.Host != "/mica/volumes/web/data" || !v.Make {
		t.Fatalf("named volume: %+v", v)
	}
	ps := r.sv.Projects()
	if len(ps) != 1 || ps[0].Name != "web" || len(ps[0].Services) != 2 || ps[0].Compose != "" || ps[0].ProjectDir != "/mica/volumes/web" {
		t.Fatalf("projects %+v", ps)
	}
	if p, err := r.sv.Project("web"); err != nil || p.Compose != webProject {
		t.Fatalf("project %+v %v", p, err)
	}
	if _, err := r.sv.Project("none"); !errors.Is(err, ErrNoProject) {
		t.Fatalf("an unknown project: %v", err)
	}
}

// A project declared with its project directory keeps it, across a restart.
func TestAProjectDirectory(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	if err := r.sv.PutProject(ctx(), "site", store.Project{Compose: []byte("services:\n  app: {image: i, volumes: [./data:/d]}\n"), Dir: dir}); err != nil {
		t.Fatal(err)
	}
	r.restart("boot-1")
	p, err := r.sv.Project("site")
	if err != nil || p.ProjectDir != dir || p.Services[0].Spec.Volumes[0].Host != dir+"/data" {
		t.Fatalf("%+v %v", p, err)
	}
}

// A container with devices is run anew at each start, so a device link names the
// node it has now; a device that went is a start failure, under the restart policy.
func TestAContainerWithDevicesIsRunAnewAtEachStart(t *testing.T) {
	r := newRig(t)
	r.project("tty", "services:\n  app: {image: i, restart: always, devices: [\"/dev/null:/dev/x\"]}\n")
	r.pass()
	r.runs("tty-app-1")
	r.en.exit("tty-app-1", 1)
	r.pass()
	r.clock.addSeconds(spec.BackoffSeconds)
	r.pass()
	if r.en.count("run tty-app-1") != 2 || r.en.count("start tty-app-1") != 0 {
		t.Fatalf("calls %v", r.en.calls)
	}
	r.en.exit("tty-app-1", 1)
	r.en.failRun = errors.New("device /dev/null is absent")
	r.pass()
	r.clock.addSeconds(spec.BackoffSeconds)
	r.pass()
	if st, _ := r.sv.Get("tty-app-1"); st.Phase != PhaseBackoff || !strings.Contains(st.Error, "is absent") {
		t.Fatalf("%+v", st)
	}
}

func TestReplacingAProjectRemovesTheServicesItDropped(t *testing.T) {
	r := newRig(t)
	r.project("web", webProject)
	r.pass()
	r.project("web", "services:\n  app: {image: docker.io/library/nginx:1}\n")
	r.pass()
	if _, ok := r.en.containers["web-db-1"]; ok {
		t.Fatal("a dropped service kept its container")
	}
	if _, err := r.sv.Get("web-db-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a dropped service is still declared: %v", err)
	}
	if got, _ := r.st.Projects(); string(got["web"].Compose) != "services:\n  app: {image: docker.io/library/nginx:1}\n" {
		t.Fatalf("stored %q", got)
	}
}

func TestPutProjectsDeclaresExactlyTheSet(t *testing.T) {
	r := newRig(t)
	r.put(web(spec.PolicyNo)) // project web
	r.project("old", "services:\n  a: {image: i}\n")
	r.pass()
	bad := map[string]store.Project{"new": {Compose: []byte("services:\n  a: {image: i}\n")}, "worse": {Compose: []byte("services:\n  a: {image: i, privileged: true}\n")}}
	var ce *compose.Error
	if err := r.sv.PutProjects(ctx(), bad); !errors.As(err, &ce) || ce.Problems[0].Project != "worse" {
		t.Fatalf("a set with a refused project: %v", err)
	}
	if len(r.sv.List()) != 2 {
		t.Fatal("a refused set changed the declarations")
	}
	if err := r.sv.PutProjects(ctx(), map[string]store.Project{"new": {Compose: []byte("services:\n  a: {image: i}\n")}}); err != nil {
		t.Fatal(err)
	}
	r.pass()
	if list := r.sv.List(); len(list) != 1 || list[0].Spec.Name != "new-a-1" {
		t.Fatalf("declared %+v", list)
	}
	for _, n := range []string{"web", "old-a-1"} {
		if _, ok := r.en.containers[n]; ok {
			t.Fatalf("%s outside the set kept its container", n)
		}
	}
	if _, ok := r.en.networks["old_default"]; ok || r.en.count("network rm old_default") != 1 {
		t.Fatalf("the network of a removed project: %v", r.en.calls)
	}
	if got, _ := r.st.Projects(); len(got) != 1 || got["new"].Compose == nil {
		t.Fatalf("stored %+v", got)
	}
}

func TestDeleteProjectRemovesItsContainersAndNetwork(t *testing.T) {
	r := newRig(t)
	r.project("web", webProject)
	r.pass()
	if err := r.sv.DeleteProject("web"); err != nil {
		t.Fatal(err)
	}
	r.pass()
	if len(r.en.containers) != 0 || len(r.en.networks) != 0 || len(r.sv.Projects()) != 0 {
		t.Fatalf("left %v %v", r.en.containers, r.en.networks)
	}
	if err := r.sv.DeleteProject("web"); !errors.Is(err, ErrNoProject) {
		t.Fatalf("deleting twice: %v", err)
	}
}

func TestTwoProjectsNamingOneContainerAreRefused(t *testing.T) {
	r := newRig(t)
	r.project("a-b", "services:\n  c: {image: i}\n")
	var se *spec.Error
	if err := r.sv.PutProject(ctx(), "a", store.Project{Compose: []byte("services:\n  b-c: {image: i}\n")}); !errors.As(err, &se) || se.Rule != "name" {
		t.Fatalf("a container name of another project: %v", err)
	}
	err := r.sv.PutProjects(ctx(), map[string]store.Project{"a-b": {Compose: []byte("services:\n  c: {image: i}\n")}, "a": {Compose: []byte("services:\n  b-c: {image: i}\n")}})
	if !errors.As(err, &se) || se.Rule != "name" {
		t.Fatalf("a set naming one container twice: %v", err)
	}
}

func TestANameAContainerRunByHandHoldsIsRefused(t *testing.T) {
	r := newRig(t)
	held := r.en.hand("core")
	var se *spec.Error
	err := r.sv.PutProject(ctx(), "web", store.Project{Compose: []byte("services:\n  app: {image: i, container_name: core}\n")})
	if !errors.As(err, &se) || se.Rule != "name" || !strings.Contains(se.Detail, held.ID[:12]) {
		t.Fatalf("a name held by hand: %v", err)
	}
	err = r.sv.PutProjects(ctx(), map[string]store.Project{"web": {Compose: []byte("services:\n  app: {image: i, container_name: core}\n")}})
	if !errors.As(err, &se) || se.Rule != "name" {
		t.Fatalf("a set naming a name held by hand: %v", err)
	}
	r.pass()
	if len(r.sv.List()) != 0 || r.en.containers["core"] != held || r.en.count("remove core") != 0 {
		t.Fatalf("calls %v", r.en.calls)
	}
}

func TestAContainerNameOfAnotherProjectIsRefused(t *testing.T) {
	r := newRig(t)
	r.project("a", "services:\n  app: {image: i, container_name: core}\n")
	var se *spec.Error
	if err := r.sv.PutProject(ctx(), "b", store.Project{Compose: []byte("services:\n  x: {image: i, container_name: core}\n")}); !errors.As(err, &se) ||
		se.Rule != "name" || !strings.Contains(se.Detail, "project a") {
		t.Fatalf("another project's name: %v", err)
	}
}

// A container run by hand after its service was declared: the service waits for the
// name, and that container is never replaced.
func TestAServiceNeverReplacesAContainerHoldingItsName(t *testing.T) {
	r := newRig(t)
	r.project("web", "services:\n  app: {image: i}\n")
	held := r.en.hand("web-app-1")
	r.pass()
	st, _ := r.sv.Get("web-app-1")
	if st.Phase != PhaseWaiting || !strings.Contains(st.Error, held.ID[:12]) || r.en.count("run web-app-1") != 0 || r.en.containers["web-app-1"] != held {
		t.Fatalf("%+v, calls %v", st, r.en.calls)
	}
	delete(r.en.containers, "web-app-1") // a person removes it
	r.pass()
	if r.en.count("run web-app-1") != 1 {
		t.Fatalf("calls %v", r.en.calls)
	}
}

func TestAContainerWithoutTheLabelIsNeverTouched(t *testing.T) {
	r := newRig(t)
	r.en.hand("hand")
	r.project("web", "services:\n  app: {image: i}\n")
	r.pass()
	if err := r.sv.PutProjects(ctx(), map[string]store.Project{}); err != nil {
		t.Fatal(err)
	}
	r.pass()
	if _, ok := r.en.containers["hand"]; !ok || r.en.count("remove hand")+r.en.count("stop hand") != 0 {
		t.Fatalf("calls %v", r.en.calls)
	}
	if _, err := r.sv.Get("hand"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a hand-run container is listed: %v", err)
	}
}

func TestARebootStartsAStoppedServiceAgain(t *testing.T) {
	r := newRig(t)
	r.project("web", "services:\n  app: {image: i, restart: unless-stopped}\n")
	r.pass()
	r.runs("web-app-1")
	_ = r.sv.Stop("web-app-1")
	r.pass()
	r.restart("boot-1")
	r.pass()
	r.want("web-app-1", PhaseStopped)
	c := r.en.containers["web-app-1"]
	c.StartedAt, c.ExitedAt = bootAt().Add(-time.Hour), bootAt().Add(-time.Minute)
	r.restart("boot-2")
	r.pass()
	if st, _ := r.sv.Get("web-app-1"); st.Desired != store.Running || st.Phase != PhaseStarting {
		t.Fatalf("after a reboot: %+v", st)
	}
}

// At start the first pass removes a managed container nothing declares; a container
// without the label is never touched, and a project is not changed.
func TestAtStartAManagedContainerNothingDeclaresIsRemoved(t *testing.T) {
	r := newRig(t)
	r.project("web", "services:\n  app: {image: i}\n")
	r.pass()
	r.en.containers["nginx"] = &engine.Container{Name: "nginx", State: "running", StartedAt: r.clock.now(), Labels: map[string]string{engine.LabelManaged: "1"}}
	r.en.hand("hand")
	r.restart("boot-1")
	if _, err := r.sv.Get("nginx"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a container nothing declares is declared: %v", err)
	}
	r.pass()
	if _, ok := r.en.containers["nginx"]; ok || r.en.count("remove nginx") != 1 {
		t.Fatalf("calls %v", r.en.calls)
	}
	if _, ok := r.en.containers["hand"]; !ok || r.en.count("run web-app-1") != 1 || r.en.count("remove web-app-1") != 0 {
		t.Fatalf("calls %v", r.en.calls)
	}
}

func TestAServiceOnTheHostNetworkJoinsNoProjectNetwork(t *testing.T) {
	r := newRig(t)
	r.project("lode", "services:\n  app: {image: i, container_name: core, network_mode: host, cap_add: [NET_ADMIN]}\n")
	r.pass()
	if r.en.count("run core") != 1 || len(r.en.networks) != 0 {
		t.Fatalf("calls %v, networks %v", r.en.calls, r.en.networks)
	}
	if st, err := r.sv.Get("core"); err != nil || st.Project != "lode" || st.Service != "app" {
		t.Fatalf("%+v %v", st, err)
	}
}

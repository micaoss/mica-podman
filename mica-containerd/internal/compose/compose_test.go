package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
)

func load(t *testing.T, file string) ([]spec.Spec, error) {
	t.Helper()
	return Load(context.Background(), "p", []byte(file), "", "/mica/volumes")
}

// tempDir is a new directory, named with its links resolved.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// one loads a file of one service, which must be accepted.
func one(t *testing.T, file string) spec.Spec {
	t.Helper()
	specs, err := load(t, file)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("%d specs", len(specs))
	}
	return specs[0]
}

// refused loads a file that must be refused with a problem of field whose detail
// holds want.
func refused(t *testing.T, file, field, want string) {
	t.Helper()
	_, err := load(t, file)
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("accepted, or not a compose error: %v", err)
	}
	for _, p := range ce.Problems {
		if p.Field == field && strings.Contains(p.Detail, want) {
			return
		}
	}
	t.Fatalf("no problem of %s with %q in %+v", field, want, ce.Problems)
}

func TestAServiceBecomesItsContainer(t *testing.T) {
	s := one(t, `services:
  app:
    image: docker.io/library/nginx:1
    command: nginx -g "daemon off;"
    entrypoint: [/docker-entrypoint.sh]
    environment: {A: "1", B: two}
`)
	want := spec.Spec{Name: "p-app-1", Project: "p", Service: "app", Image: "docker.io/library/nginx:1",
		Entrypoint: []string{"/docker-entrypoint.sh"}, Command: []string{"nginx", "-g", "daemon off;"},
		Environment: map[string]string{"A": "1", "B": "two"}, Network: "p_default", Aliases: []string{"app"},
		Restart: spec.Restart{Policy: spec.PolicyNo}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("\n got %+v\nwant %+v", s, want)
	}
}

// A file written for docker compose, loaded unchanged, from its directory.
func TestAHostNetworkServiceWithItsOwnName(t *testing.T) {
	dir := filepath.Join(tempDir(t), "bit-56-core")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	specs, err := Load(context.Background(), "p", []byte(`services:
  app:
    image: docker.io/bkhq/lode
    container_name: core
    restart: unless-stopped
    network_mode: host
    cap_add: [NET_ADMIN]
    volumes: ["./data:/srv/lode"]
`), dir, "/mica/volumes")
	if err != nil {
		t.Fatal(err)
	}
	want := spec.Spec{Name: "core", Project: "p", Service: "app", Image: "docker.io/bkhq/lode", NetworkMode: spec.NetworkHost,
		CapAdd: []string{"CAP_NET_ADMIN"}, Volumes: []spec.Volume{{Host: dir + "/data", Container: "/srv/lode", Make: true}},
		Restart: spec.Restart{Policy: spec.PolicyAlways}}
	if !reflect.DeepEqual(specs[0], want) {
		t.Fatalf("\n got %+v\nwant %+v", specs[0], want)
	}
}

func TestContainerNameNetworkModeAndCapabilities(t *testing.T) {
	specs, err := load(t, `services:
  db: {image: i, container_name: store, network_mode: none, cap_drop: [ALL]}
  app: {image: i, container_name: web, depends_on: [db], cap_add: [cap_net_raw, CAP_SYS_TIME, sys_nice], cap_drop: [MKNOD]}
`)
	if err != nil {
		t.Fatal(err)
	}
	app, db := specs[1], specs[0]
	if app.Name != "web" || app.Network != "p_default" || !reflect.DeepEqual(app.Aliases, []string{"app"}) || app.NetworkMode != "" ||
		!reflect.DeepEqual(app.DependsOn, []spec.Dependency{{Name: "store", Ready: spec.ReadyRunning}}) ||
		!reflect.DeepEqual(app.CapAdd, []string{"CAP_NET_RAW", "CAP_SYS_TIME", "CAP_SYS_NICE"}) || !reflect.DeepEqual(app.CapDrop, []string{"CAP_MKNOD"}) {
		t.Fatalf("app %+v", app)
	}
	if db.Name != "store" || db.NetworkMode != spec.NetworkNone || db.Network != "" || db.Aliases != nil || !reflect.DeepEqual(db.CapDrop, []string{"ALL"}) {
		t.Fatalf("db %+v", db)
	}
}

func TestUserAndWorkingDir(t *testing.T) {
	for user, ok := range map[string]bool{"1000": true, "1000:1000": true, "nginx": true, "www-data:www-data": true, "-1": false, "a b": false, "a:b:c": false} {
		_, err := load(t, "services:\n  app: {image: i, user: \""+user+"\", working_dir: /srv/app}\n")
		if (err == nil) != ok {
			t.Errorf("user %q: %v", user, err)
		}
	}
	s := one(t, "services:\n  app: {image: i, user: \"1000:1000\", working_dir: /srv/app}\n")
	if s.User != "1000:1000" || s.WorkingDir != "/srv/app" {
		t.Fatalf("%+v", s)
	}
}

func TestEnvironmentListAndEnvFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p", "app.env"), []byte("FROM_FILE=1\nA=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	specs, err := Load(context.Background(), "p", []byte(`services:
  app:
    image: i
    env_file: app.env
    environment: [A=written]
`), "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if env := specs[0].Environment; env["FROM_FILE"] != "1" || env["A"] != "written" {
		t.Fatalf("environment %v", env)
	}
	_, err = Load(context.Background(), "p", []byte("services:\n  app: {image: i, env_file: missing.env}\n"), "", dir)
	if err == nil {
		t.Fatal("a missing required env_file was accepted")
	}
	specs, err = Load(context.Background(), "p", []byte("services:\n  app: {image: i, env_file: [{path: missing.env, required: false}]}\n"), "", dir)
	if err != nil || len(specs) != 1 {
		t.Fatalf("an optional missing env_file: %v", err)
	}
}

func TestPorts(t *testing.T) {
	s := one(t, `services:
  app:
    image: i
    ports: ["8080:80", "5353:53/udp", "9000-9001:9000-9001", {target: 443, published: "8443", protocol: tcp}]
`)
	want := []spec.Port{{Host: 8080, Container: 80, Protocol: "tcp"}, {Host: 5353, Container: 53, Protocol: "udp"},
		{Host: 9000, Container: 9000, Protocol: "tcp"}, {Host: 9001, Container: 9001, Protocol: "tcp"}, {Host: 8443, Container: 443, Protocol: "tcp"}}
	if !reflect.DeepEqual(s.Publish, want) {
		t.Fatalf("ports %+v", s.Publish)
	}
}

func TestVolumes(t *testing.T) {
	tmp := tempDir(t)
	dir := filepath.Join(tmp, "p")
	for _, d := range []string{dir, filepath.Join(tmp, "apps", "x")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(tmp, "apps"), filepath.Join(tmp, "link")); err != nil {
		t.Fatal(err)
	}
	specs, err := Load(context.Background(), "p", []byte(`services:
  app:
    image: i
    volumes:
      - data:/data
      - ./conf:/etc/app:ro
      - `+tmp+`/apps/web:/html
      - {type: bind, source: `+tmp+`/apps/x, target: /x, read_only: true}
      - ../link/x:/l
      - ../link/new:/n
volumes:
  data:
`), dir, "/mica/volumes")
	if err != nil {
		t.Fatal(err)
	}
	want := []spec.Volume{
		{Host: "/mica/volumes/p/data", Container: "/data", Make: true},
		{Host: dir + "/conf", Container: "/etc/app", ReadOnly: true, Make: true},
		{Host: tmp + "/apps/web", Container: "/html", Make: true},
		{Host: tmp + "/apps/x", Container: "/x", ReadOnly: true},
		{Host: tmp + "/apps/x", Container: "/l", Make: true},
		{Host: tmp + "/apps/new", Container: "/n", Make: true},
	}
	if !reflect.DeepEqual(specs[0].Volumes, want) {
		t.Fatalf("volumes\n got %+v\nwant %+v", specs[0].Volumes, want)
	}
}

// A host path that is not there is made when it can be; the long form makes none.
func TestAMissingHostPath(t *testing.T) {
	tmp := tempDir(t)
	file := filepath.Join(tmp, "file")
	_ = os.WriteFile(file, nil, 0o600)
	_ = os.Symlink(filepath.Join(tmp, "nothing"), filepath.Join(tmp, "dangling"))
	locked := filepath.Join(tmp, "locked")
	_ = os.Mkdir(locked, 0o555)
	for _, c := range []struct{ volume, want string }{
		{"{type: bind, source: ./missing, target: /m}", tmp + "/missing does not exist"},
		{"\"./file/sub:/s\"", "cannot be made: " + file + " is not a directory"},
		{"\"./dangling:/d\"", tmp + "/dangling: "},
		{"\"./locked/sub:/s\"", "cannot be made: " + locked + ": permission denied"},
	} {
		if strings.Contains(c.volume, "locked") && os.Geteuid() == 0 {
			continue // root writes any directory but on a read-only filesystem
		}
		_, err := Load(context.Background(), "p", []byte("services:\n  app: {image: i, volumes: ["+c.volume+"]}\n"), tmp, "/mica/volumes")
		var ce *Error
		if !errors.As(err, &ce) || ce.Problems[0].Field != "volumes" || !strings.Contains(ce.Problems[0].Detail, c.want) {
			t.Errorf("%s: %v, want %q", c.volume, err, c.want)
		}
	}
}

func TestDevices(t *testing.T) {
	tmp := tempDir(t)
	link := filepath.Join(tmp, "by-id")
	if err := os.Symlink("/dev/null", link); err != nil {
		t.Fatal(err)
	}
	s := one(t, `services:
  app:
    image: i
    devices:
      - /dev/null
      - /dev/null:/dev/x
      - /dev/null:/dev/y:r
      - {source: /dev/null, target: /dev/z, permissions: rw}
      - `+link+`:/dev/ttyS1
    device_cgroup_rules: ["c 188:* rwm"]
`)
	want := []spec.Device{{Host: "/dev/null", Container: "/dev/null", Permissions: "rwm"}, {Host: "/dev/null", Container: "/dev/x", Permissions: "rwm"},
		{Host: "/dev/null", Container: "/dev/y", Permissions: "r"}, {Host: "/dev/null", Container: "/dev/z", Permissions: "rw"},
		{Host: link, Container: "/dev/ttyS1", Permissions: "rwm"}} // the link: resolved at each start
	if !reflect.DeepEqual(s.Devices, want) || !reflect.DeepEqual(s.DeviceCgroupRules, []string{"c 188:* rwm"}) {
		t.Fatalf("devices %+v %v", s.Devices, s.DeviceCgroupRules)
	}
}

func TestRestart(t *testing.T) {
	for restart, want := range map[string]spec.Restart{
		"":               {Policy: spec.PolicyNo},
		"no":             {Policy: spec.PolicyNo},
		"always":         {Policy: spec.PolicyAlways},
		"unless-stopped": {Policy: spec.PolicyAlways},
		"on-failure":     {Policy: spec.PolicyOnFailure},
		"on-failure:5":   {Policy: spec.PolicyOnFailure, MaxRestarts: 5},
	} {
		file := "services:\n  app: {image: i}\n"
		if restart != "" {
			file = "services:\n  app: {image: i, restart: \"" + restart + "\"}\n"
		}
		if got := one(t, file).Restart; got != want {
			t.Errorf("%q: %+v, want %+v", restart, got, want)
		}
	}
}

func TestDependsOnAndHealthcheck(t *testing.T) {
	specs, err := load(t, `services:
  db:
    image: i
    healthcheck: {test: ["CMD-SHELL", "pg_isready -U x"], interval: 10s, timeout: 3s, retries: 5, start_period: 20s}
  cache:
    image: i
    healthcheck: {test: [CMD, redis-cli, ping]}
  plain:
    image: i
    healthcheck: {test: [NONE]}
  app:
    image: i
    depends_on:
      db: {condition: service_healthy}
      cache: {condition: service_started}
      plain:
        condition: service_started
`)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]spec.Spec{}
	for _, s := range specs {
		by[s.Service] = s
	}
	if d := by["app"].DependsOn; !reflect.DeepEqual(d, []spec.Dependency{{Name: "p-cache-1", Ready: "running"}, {Name: "p-db-1", Ready: "healthy"}, {Name: "p-plain-1", Ready: "running"}}) {
		t.Fatalf("depends_on %+v", d)
	}
	if h := by["db"].Health; !reflect.DeepEqual(h, &spec.Health{Command: []string{"/bin/sh", "-c", "pg_isready -U x"}, IntervalSeconds: 10, TimeoutSeconds: 3, Retries: 5, StartPeriodSeconds: 20}) {
		t.Fatalf("db health %+v", h)
	}
	if h := by["cache"].Health; h == nil || !reflect.DeepEqual(h.Command, []string{"redis-cli", "ping"}) || h.IntervalSeconds != 0 {
		t.Fatalf("cache health %+v", h)
	}
	if by["plain"].Health != nil {
		t.Fatal("test NONE kept a health check")
	}
}

func TestLimits(t *testing.T) {
	s := one(t, "services:\n  app: {image: i, mem_limit: 512m, cpus: 0.5, pids_limit: 100}\n")
	if s.Limits != (spec.Limits{Pids: 100, Memory: "512m", CPU: "0.5"}) {
		t.Fatalf("limits %+v", s.Limits)
	}
	s = one(t, "services:\n  app:\n    image: i\n    deploy: {replicas: 1, resources: {limits: {memory: 1g, cpus: '2', pids: 50}}}\n")
	if s.Limits != (spec.Limits{Pids: 50, Memory: "1g", CPU: "2"}) {
		t.Fatalf("deploy limits %+v", s.Limits)
	}
}

func TestInterpolation(t *testing.T) {
	s := one(t, "services:\n  app: {image: \"nginx:${TAG:-1.29}\", environment: {A: \"$${literal}\"}}\n")
	if s.Image != "nginx:1.29" || s.Environment["A"] != "${literal}" {
		t.Fatalf("%+v", s)
	}
	_, err := load(t, "services:\n  app: {image: \"nginx:${TAG}\"}\n")
	if err == nil || !strings.Contains(err.Error(), "TAG") {
		t.Fatalf("a variable without a default: %v", err)
	}
}

func TestTheProjectAndItsNames(t *testing.T) {
	one(t, "name: p\nversion: \"3.8\"\nx-common: {a: 1}\nservices:\n  app: {image: i, x-note: n}\nnetworks:\n  default: {}\n")
	refused(t, "name: other\nservices:\n  app: {image: i}\n", "name", "names project other")
	refused(t, "services:\n  App: {image: i}\n", "name", "service \"App\"")
	refused(t, "services:\n  a-very-long-service-name: {image: i}\n", "name", "1 to 20")
	refused(t, "services: {}\n", "services", "no service")
	if _, err := Load(context.Background(), "Bad", []byte("services:\n  a: {image: i}\n"), "", "/mica/volumes"); err == nil {
		t.Fatal("a bad project name was accepted")
	}
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct{ file, field, want string }{
		{"services:\n  app: {build: .}\n", "build", "not supported"},
		{"services:\n  app: {image: i, privileged: true}\n", "privileged", "not supported"},
		{"services:\n  app: {image: i, devices: [/dev/mica-absent]}\n", "devices", "device /dev/mica-absent is absent"},
		{"services:\n  app: {image: i, devices: [/etc/passwd]}\n", "devices", "/etc/passwd is not a device"},
		{"services:\n  app: {image: i, devices: [dev/null]}\n", "devices", "not an absolute path"},
		{"services:\n  app: {image: i, devices: [\"/dev/null:/dev/x:rwx\"]}\n", "devices", "permissions \"rwx\""},
		{"services:\n  app: {image: i, devices: [\"/dev/null:x\"]}\n", "devices", "clean absolute path"},
		{"services:\n  app: {image: i, devices: [{source: /dev/null, mode: x}]}\n", "devices.mode", "not supported"},
		{"services:\n  app: {image: i, device_cgroup_rules: [\"x 1:2 r\"]}\n", "devices", "device cgroup rule"},
		{"services:\n  app: {image: i, network_mode: bridge}\n", "network_mode", "not host or none"},
		{"services:\n  db: {image: i}\n  app: {image: i, network_mode: \"service:db\"}\n", "network_mode", "not host or none"},
		{"services:\n  app: {image: i, network_mode: host, ports: [\"80:80\"]}\n", "ports", "host network publishes no port"},
		{"services:\n  app: {image: i, network_mode: none, networks: [default]}\n", "networks", "beside network_mode"},
		{"services:\n  app: {image: i, cap_add: [NET_WIZARD]}\n", "cap_add", "not a capability"},
		{"services:\n  app: {image: i, cap_drop: [\"\"]}\n", "cap_drop", "not a capability"},
		{"services:\n  app: {image: i, user: \"1000 0\"}\n", "user", "is not a user"},
		{"services:\n  app: {image: i, working_dir: app}\n", "working_dir", "clean absolute path"},
		{"services:\n  app: {image: i, container_name: Core}\n", "container_name", "not 1 to 63"},
		{"services:\n  a: {image: i, container_name: core}\n  b: {image: i, container_name: core}\n", "container_name", "also the container of service a"},
		{"services:\n  a: {image: i, container_name: p-b-1}\n  b: {image: i}\n", "container_name", "also the container of service"},
		{"services:\n  app: {image: i, labels: {a: b}}\n", "labels", "not supported"},
		{"services:\n  app: {image: i, secrets: [s]}\nsecrets:\n  s: {file: ./s}\n", "secrets", "not supported"},
		{"include: [other.yaml]\nservices:\n  app: {image: i}\n", "include", "not supported"},
		{"services:\n  app: {command: x}\n", "compose", "neither an image"},
		{"services:\n  app: {image: i, environment: [BARE]}\n", "environment", "BARE has no value"},
		{"services:\n  app: {image: i, env_file: ~/.profile}\n", "env_file", "a ~ path is not supported"},
		{"services:\n  app: {image: i, volumes: [~/x:/x]}\n", "volumes", "a ~ path is not supported"},
		{"services:\n  app: {image: i, ports: [\"80\"]}\n", "ports", "no published port"},
		{"services:\n  app: {image: i, ports: [\"127.0.0.1:8080:80\"]}\n", "ports", "host address"},
		{"services:\n  app: {image: i, ports: [{target: 80, published: \"80\", mode: host}]}\n", "ports.mode", "mode host"},
		{"services:\n  app: {image: i, ports: [{target: 80, published: \"80\", host_ip: 127.0.0.1}]}\n", "ports.host_ip", "not supported"},
		{"services:\n  app: {image: i, volumes: [/data]}\n", "volumes", "anonymous"},
		{"services:\n  app: {image: i, volumes: [{type: tmpfs, target: /tmp}]}\n", "volumes", "tmpfs"},
		{"services:\n  app: {image: i, volumes: [{type: volume, source: d, target: /d, volume: {nocopy: true}}]}\nvolumes:\n  d: {}\n", "volumes.volume", "not supported"},
		{"services:\n  app: {image: i, volumes: [d:/d]}\nvolumes:\n  d: {external: true}\n", "volumes.d.external", "not supported"},
		{"services:\n  app: {image: i, restart: sometimes}\n", "restart", "sometimes"},
		{"services:\n  app: {image: i, restart: \"on-failure:0\"}\n", "restart", "at least 1"},
		{"services:\n  db: {image: i}\n  app: {image: i, depends_on: {db: {condition: service_completed_successfully}}}\n", "depends_on", "service_completed_successfully"},
		{"services:\n  db: {image: i}\n  app: {image: i, depends_on: {db: {condition: service_started, required: false}}}\n", "depends_on", "required: false"},
		{"services:\n  db: {image: i}\n  app: {image: i, depends_on: {db: {condition: service_started, restart: true}}}\n", "depends_on", "restart: true"},
		{"services:\n  app: {image: i, healthcheck: {test: [CMD, x], start_interval: 1s}}\n", "healthcheck.start_interval", "not supported"},
		{"services:\n  app: {image: i, healthcheck: {test: [CMD, x], interval: 1500ms}}\n", "healthcheck", "whole number of seconds"},
		{"services:\n  app: {image: i, healthcheck: {test: [CMD, x], interval: 5s, timeout: 10s}}\n", "health", "exceeds the interval"},
		{"services:\n  app: {image: i, deploy: {replicas: 3}}\n", "deploy", "replicas 3"},
		{"services:\n  app: {image: i, deploy: {resources: {reservations: {memory: 1g}}}}\n", "deploy.resources.reservations", "not supported"},
		{"services:\n  app: {image: i, deploy: {placement: {}}}\n", "deploy.placement", "not supported"},
		{"services:\n  app: {image: i, mem_limit: 512m, deploy: {resources: {limits: {memory: 1g}}}}\n", "compose", "distinct values"},
		{"services:\n  app: {image: i, mem_limit: 1000b}\n", "mem_limit", "whole number of KiB"},
		{"services:\n  app: {image: i, networks: [back]}\nnetworks:\n  back: {}\n", "networks", "network back"},
		{"services:\n  app: {image: i, networks: {default: {aliases: [x]}}}\n", "networks", "without options"},
	} {
		t.Run(c.field, func(t *testing.T) { refused(t, c.file, c.field, c.want) })
	}
}

func TestEveryProblemIsReported(t *testing.T) {
	_, err := load(t, "services:\n  a: {image: i, privileged: true, labels: {x: y}}\n  b: {image: i, security_opt: [x]}\n")
	var ce *Error
	if !errors.As(err, &ce) || len(ce.Problems) != 3 {
		t.Fatalf("%v", err)
	}
	if ce.Error() != "a: labels is not supported; a: privileged is not supported; b: security_opt is not supported" {
		t.Fatalf("detail %q", ce.Error())
	}
}

// A relative host path is in the project directory: the one given, else the
// project's under volumes. Any absolute host path is accepted.
func TestARelativeBindIsInTheProjectDirectory(t *testing.T) {
	tmp := tempDir(t)
	specs, err := Load(context.Background(), "p", []byte("services:\n  app: {image: i, volumes: [./data:/d]}\n"), "", tmp)
	if err != nil || specs[0].Volumes[0].Host != tmp+"/p/data" {
		t.Fatalf("%+v %v", specs, err)
	}
	specs, err = Load(context.Background(), "p", []byte("services:\n  app: {image: i, volumes: [./data:/d, \"/etc:/host-etc:ro\"]}\n"), tmp+"/site", tmp)
	if err != nil || specs[0].Volumes[0].Host != tmp+"/site/data" || specs[0].Volumes[1].Host != "/etc" {
		t.Fatalf("%+v %v", specs, err)
	}
	var ce *Error
	if _, err := Load(context.Background(), "p", []byte("services:\n  app: {image: i}\n"), "site", tmp); !errors.As(err, &ce) || ce.Problems[0].Field != "projectDir" {
		t.Fatalf("a relative project directory: %v", err)
	}
}

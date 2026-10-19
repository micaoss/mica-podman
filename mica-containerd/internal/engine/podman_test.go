package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
)

func TestRunArgs(t *testing.T) {
	p := &Podman{Bin: "podman", LogDir: "/run/mica-containerd/logs", LogMaxSize: "1mb"}
	s := spec.Spec{
		Name:        "web",
		Project:     "site",
		Service:     "web",
		Network:     "site_default",
		Aliases:     []string{"web"},
		Image:       "docker.io/library/nginx:1.29",
		Command:     []string{"nginx", "-g", "daemon off;"},
		Environment: map[string]string{"B": "2", "A": "x=y z"},
		Publish:     []spec.Port{{Host: 8080, Container: 80}, {Host: 53, Container: 53, Protocol: "udp"}},
		Volumes:     []spec.Volume{{Host: "/mica/data/web", Container: "/html", ReadOnly: true}, {Host: "/mica/data/tmp", Container: "/tmp"}},
		Limits:      spec.Limits{Pids: 128, Memory: "64m", CPU: "0.5"},
		Restart:     spec.Restart{Policy: spec.PolicyAlways},
	}
	want := []string{
		"run", "--detach", "--replace", "--name", "web",
		"--label", "mica.containerd=1",
		"--label", "mica.containerd.spec=" + s.Hash(),
		"--label", "mica.containerd.project=site",
		"--restart", "no",
		"--log-driver", "k8s-file",
		"--log-opt", "path=/run/mica-containerd/logs/web.log",
		"--log-opt", "max-size=1mb",
		"--network", "site_default", "--network-alias", "web",
		"--env", "A=x=y z", "--env", "B=2",
		"--publish", "8080:80/tcp", "--publish", "53:53/udp",
		"--volume", "/mica/data/web:/html:ro", "--volume", "/mica/data/tmp:/tmp",
		"--pids-limit", "128", "--memory", "64m", "--cpus", "0.5",
		"docker.io/library/nginx:1.29", "nginx", "-g", "daemon off;",
	}
	if got := p.RunArgs(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("run args\n got %q\nwant %q", got, want)
	}
}

func TestRunArgsLeaveDefaultsToPodman(t *testing.T) {
	p := &Podman{Bin: "podman", LogDir: "/l", LogMaxSize: "1mb"}
	got := strings.Join(p.RunArgs(spec.Spec{Name: "a", Image: "i"}), " ")
	for _, flag := range []string{"--pids-limit", "--memory", "--cpus", "--env", "--publish", "--volume"} {
		if strings.Contains(got, flag) {
			t.Errorf("%s passed for a spec that declares none", flag)
		}
	}
	if !strings.HasSuffix(got, " i") {
		t.Errorf("the image is not last: %s", got)
	}
}

// A `podman ps --all --format json` answer of podman 6.1.2, trimmed to two containers.
const psJSON = `[
 {"AutoRemove":false,"Command":["sleep","60"],"Exited":false,"ExitedAt":-62135596800,"ExitCode":0,
  "Id":"aaa","Image":"docker.io/library/busybox:1","ImageID":"img1",
  "Labels":{"mica.containerd":"1","mica.containerd.spec":"h1"},"Names":["a"],"StartedAt":1790000000,"State":"running"},
 {"Id":"bbb","ImageID":"img2","Labels":{"mica.containerd":"1"},"Names":["b"],"State":"exited","ExitCode":3,
  "StartedAt":1790000000,"ExitedAt":1790000100}
]`

func TestParsePS(t *testing.T) {
	cs, err := ParsePS([]byte(psJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Name != "a" || !cs[0].Running() || cs[0].Labels[LabelSpec] != "h1" {
		t.Fatalf("first: %+v", cs)
	}
	if !cs[0].ExitedAt.IsZero() {
		t.Fatal("podman's zero time was read as an exit")
	}
	if cs[1].Running() || cs[1].ExitCode != 3 || cs[1].ExitedAt.Unix() != 1790000100 {
		t.Fatalf("second: %+v", cs[1])
	}
}

func TestParseEvent(t *testing.T) {
	e, ok, err := ParseEvent([]byte(`{"ContainerExitCode":137,"ID":"aaa","Name":"a","Status":"died","time":1790000000,"timeNano":1790000000123456789,"Type":"container","Attributes":{"mica.containerd":"1"}}`))
	if err != nil || !ok || e.Name != "a" || e.Status != "died" || e.ExitCode == nil || *e.ExitCode != 137 || e.Time.UnixNano() != 1790000000123456789 {
		t.Fatalf("%+v %v %v", e, ok, err)
	}
	if _, ok, _ := ParseEvent([]byte(`{"Name":"x","Status":"pull","Type":"image"}`)); ok {
		t.Fatal("an image event was read as a container event")
	}
	if _, _, err := ParseEvent([]byte(`not json`)); err == nil {
		t.Fatal("garbage parsed")
	}
}

func TestRunArgsHealthCheck(t *testing.T) {
	p := &Podman{Bin: "podman", LogDir: "/l", LogMaxSize: "1mb"}
	got := strings.Join(p.RunArgs(spec.Spec{Name: "a", Image: "i",
		Health: &spec.Health{Command: []string{"wget", "-q", "http://127.0.0.1/"}, IntervalSeconds: 20}}), " ")
	want := `--health-cmd ["wget","-q","http://127.0.0.1/"] --health-interval disable --health-timeout 10s --health-retries 3 --health-start-period 0s i`
	if !strings.HasSuffix(got, want) {
		t.Fatalf("got %s\nwant the suffix %s", got, want)
	}
}

func TestRunArgsOfAService(t *testing.T) {
	p := &Podman{Bin: "podman", LogDir: "/l", LogMaxSize: "1mb"}
	s := spec.Spec{Name: "web-app-1", Project: "web", Service: "app", Image: "i", Entrypoint: []string{"/bin/sh", "-c"},
		Command: []string{"exec nginx"}, Network: "web_default", Aliases: []string{"app"}, User: "1000:1000", WorkingDir: "/srv"}
	got := strings.Join(p.RunArgs(s), " ")
	for _, want := range []string{"--label mica.containerd.project=web", "--network web_default --network-alias app",
		`--entrypoint ["/bin/sh","-c"]`, "--user 1000:1000 --workdir /srv", " i exec nginx"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
}

func TestParseNetworks(t *testing.T) {
	got, err := ParseNetworks([]byte(`[{"name":"web_default","driver":"bridge","labels":{"mica.containerd":"1","mica.containerd.project":"web"}}]`))
	if err != nil || len(got) != 1 || got[0] != (Network{Name: "web_default", Project: "web"}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestManaged(t *testing.T) {
	cs, _ := ParsePS([]byte(`[{"Id":"a","Names":["a"],"Labels":{"mica.containerd":"1"}},{"Id":"b","Names":["b"],"Labels":null}]`))
	if !cs[0].Managed() || cs[1].Managed() {
		t.Fatalf("%+v", cs)
	}
}

func TestRunArgsOnTheHostNetworkWithCapabilities(t *testing.T) {
	p := &Podman{Bin: "podman", LogDir: "/l", LogMaxSize: "1mb"}
	s := spec.Spec{Name: "core", Project: "lode", Service: "app", Image: "i", NetworkMode: spec.NetworkHost,
		CapAdd: []string{"CAP_NET_ADMIN"}, CapDrop: []string{"CAP_MKNOD"}}
	got := strings.Join(p.RunArgs(s), " ")
	if !strings.Contains(got, "--name core ") || !strings.Contains(got, "--network host --cap-add CAP_NET_ADMIN --cap-drop CAP_MKNOD") ||
		strings.Contains(got, "--network-alias") {
		t.Fatalf("%s", got)
	}
}

func TestRunArgsWithDevices(t *testing.T) {
	p := &Podman{Bin: "podman", LogDir: "/l", LogMaxSize: "1mb"}
	s := spec.Spec{Name: "tty", Project: "t", Service: "app", Image: "i", Network: "t_default",
		Devices: []spec.Device{{Host: "/dev/serial/by-id/usb-x", Container: "/dev/ttyS1", Permissions: "rw"}}, DeviceCgroupRules: []string{"c 188:* rwm"}}
	got := strings.Join(p.runArgs(s, []spec.Device{{Host: "/dev/ttyUSB0", Container: "/dev/ttyS1", Permissions: "rw"}}), " ")
	if !strings.Contains(got, "--device /dev/ttyUSB0:/dev/ttyS1:rw --device-cgroup-rule c 188:* rwm") || !strings.Contains(got, LabelSpec+"="+s.Hash()) {
		t.Fatalf("%s", got)
	}
}

// Run hands podman the node a device link names now; an absent device runs nothing.
func TestRunResolvesADeviceLink(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "podman")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" > "+args+"\n"), 0o700); err != nil { //nolint:gosec // a test script
		t.Fatal(err)
	}
	link := filepath.Join(dir, "by-id")
	_ = os.Symlink("/dev/null", link)
	p := &Podman{Bin: bin, LogDir: dir, LogMaxSize: "1mb"}
	s := spec.Spec{Name: "tty", Project: "t", Service: "app", Image: "i", Network: "t_default",
		Devices: []spec.Device{{Host: link, Container: "/dev/x", Permissions: "rwm"}}}
	if err := p.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(args); !strings.Contains(string(b), "--device /dev/null:/dev/x:rwm") {
		t.Fatalf("%s", b)
	}
	_ = os.Remove(args)
	s.Devices[0].Host = filepath.Join(dir, "gone")
	if err := p.Run(context.Background(), s); err == nil || !strings.Contains(err.Error(), "device "+s.Devices[0].Host+" is absent") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(args); err == nil {
		t.Fatal("podman ran without the device")
	}
}

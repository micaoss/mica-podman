// mica-containerd owns every container on a Mica OS device: it keeps each declared
// container in its declared state and serves the v1 API on a UNIX socket, on systemd
// and OpenRC alike.
//
//	mica-containerd [flags]        the daemon
//	mica-containerd ctl <command>  a client of its API, for a person on the device
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/micaoss/mica-podman/mica-containerd/internal/api"
	"github.com/micaoss/mica-podman/mica-containerd/internal/engine"
	"github.com/micaoss/mica-podman/mica-containerd/internal/store"
	"github.com/micaoss/mica-podman/mica-containerd/internal/supervisor"
)

// version is set by the build (-ldflags "-X main.version=...").
var version = "dev"

const defaultSocket = "/run/mica-containerd/api.sock"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(ctl(os.Args[2:], os.Stdout, os.Stderr))
	}
	if err := daemon(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mica-containerd:", err)
		os.Exit(1)
	}
}

func daemon(args []string) error {
	fs := flag.NewFlagSet("mica-containerd", flag.ContinueOnError)
	socket := fs.String("socket", defaultSocket, "the API socket")
	storeDir := fs.String("store", "/var/lib/mica/containerd", "the declarations, on STATE")
	logDir := fs.String("log-dir", "/run/mica-containerd/logs", "the containers' logs, on /run")
	logMax := fs.String("log-max-size", "1mb", "the size cap of one container's log")
	podman := fs.String("podman", "/usr/bin/podman", "the podman binary")
	resync := fs.Duration("resync", time.Minute, "the period of the full pass")
	stopTimeout := fs.Duration("stop-timeout", 10*time.Second, "how long a stopping container gets before it is killed")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	bootTime, err := readBootTime("/proc/stat")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*logDir, 0o755); err != nil { //nolint:gosec // logs are read through the API, root only
		return err
	}
	st, err := store.Open(*storeDir)
	if err != nil {
		return err
	}
	sv, err := supervisor.New(st, &engine.Podman{Bin: *podman, LogDir: *logDir, LogMaxSize: *logMax}, supervisor.Options{
		BootID: strings.TrimSpace(string(bootID)), BootTime: bootTime, Resync: *resync, StopTimeout: *stopTimeout, Log: log,
	})
	if err != nil {
		return err
	}
	listener, err := api.Listen(*socket)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler: api.Handler(sv, api.Info{Version: version, Store: st.Dir(), BootID: strings.TrimSpace(string(bootID)), Started: time.Now()}),
		// No write timeout: logs and events stream.
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	supervised := make(chan error, 1)
	go func() { supervised <- sv.Run(ctx) }()
	log.Info("mica-containerd serving", "version", version, "socket", *socket, "store", st.Dir())

	select {
	case <-ctx.Done():
	case err = <-served:
	case err = <-supervised:
	}
	cancel()
	// Containers keep running: stopping the supervisor is not stopping them.
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = srv.Shutdown(shutdown) //nolint:errcheck // leaving anyway
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("mica-containerd stopped; its containers keep running")
	return nil
}

// readBootTime is the btime line of /proc/stat.
func readBootTime(path string) (time.Time, error) {
	b, err := os.ReadFile(path) //nolint:gosec // a fixed path
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("%s: btime %q", path, v)
			}
			return time.Unix(n, 0), nil
		}
	}
	return time.Time{}, fmt.Errorf("%s has no btime line", path)
}

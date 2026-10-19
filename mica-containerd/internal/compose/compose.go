// Package compose turns a Compose file into the specs of its containers: parsed with
// compose-go, the parser of docker compose, then held to the subset the supervisor
// carries out. Anything else written in the file is refused, never ignored.
package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/compose-spec/compose-go/v2/interpolation"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/sirupsen/logrus"
	"go.yaml.in/yaml/v4"

	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
)

// Problem is one thing of a file refused: of a service, or of the project when Service
// is empty. Project is set when a set of projects was declared.
type Problem struct {
	Project string `json:"project,omitempty"`
	Service string `json:"service"`
	Field   string `json:"field"`
	Detail  string `json:"detail"`
}

// Error is a file refused, with every problem found.
type Error struct{ Problems []Problem }

func (e *Error) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.String()
	}
	return strings.Join(parts, "; ")
}

func (p Problem) String() string {
	prefix := p.Project
	if p.Service != "" {
		prefix = strings.TrimPrefix(prefix+"/"+p.Service, "/")
	}
	if prefix == "" {
		return p.Detail
	}
	return prefix + ": " + p.Detail
}

var (
	projectRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	serviceRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,19}$`)
)

// ValidProject reports whether name can name a project.
func ValidProject(name string) bool { return projectRE.MatchString(name) }

// ContainerName is the container of a service, as compose names it.
func ContainerName(project, service string) string { return project + "-" + service + "-1" }

// NetworkName is the network of a project, as compose names it.
func NetworkName(project string) string { return project + "_default" }

// Dir is the directory of a project's named volumes under volumes, and its project
// directory when none is given.
func Dir(volumes, project string) string { return path.Join(volumes, project) }

// The keys read, at each level; every other key but an x- one is refused.
var (
	topKeys     = keys("name", "services", "volumes", "networks", "version")
	serviceKeys = keys("image", "command", "entrypoint", "environment", "env_file", "ports", "volumes", "restart",
		"depends_on", "healthcheck", "mem_limit", "cpus", "pids_limit", "deploy", "networks", "expose", "user", "working_dir",
		"container_name", "network_mode", "cap_add", "cap_drop", "devices", "device_cgroup_rules")
	portKeys      = keys("target", "published", "protocol", "mode")
	volumeKeys    = keys("type", "source", "target", "read_only")
	envFileKeys   = keys("path", "required")
	deviceKeys    = keys("source", "target", "permissions")
	dependsKeys   = keys("condition", "required", "restart")
	healthKeys    = keys("test", "interval", "timeout", "retries", "start_period", "disable")
	deployKeys    = keys("resources", "replicas")
	resourcesKeys = keys("limits")
	limitsKeys    = keys("cpus", "memory", "pids")
)

func keys(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

var quiet sync.Once

// Load turns the Compose file of project into the specs of its containers, sorted by
// name. Relative host paths are in dir, the project directory, or in Dir(volumes,
// project) when dir is empty; named volumes are in Dir(volumes, project). Host paths
// are named with every link resolved. A refused file is an *Error.
func Load(ctx context.Context, project string, file []byte, dir, volumes string) ([]spec.Spec, error) {
	quiet.Do(func() { logrus.SetOutput(io.Discard) }) // compose-go warns through logrus
	c := &checker{project: project, dir: dir, volumes: Dir(volumes, project)}
	if dir == "" {
		c.dir = c.volumes
	}
	if !ValidProject(project) {
		c.add("", "name", "project %q is not 1 to 40 of [a-z0-9_-], starting with a letter or digit", project)
		return nil, c.err()
	}
	if !path.IsAbs(c.dir) || path.Clean(c.dir) != c.dir {
		c.add("", "projectDir", "%q is not a clean absolute path", c.dir)
		return nil, c.err()
	}
	var raw map[string]any
	if err := yaml.Unmarshal(file, &raw); err != nil {
		c.add("", "yaml", "%v", err)
		return nil, c.err()
	}
	c.raw(raw)
	if len(c.problems) > 0 {
		return nil, c.err()
	}
	p, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir:  c.dir,
		ConfigFiles: []types.ConfigFile{{Filename: path.Join(c.dir, "compose.yaml"), Content: file}},
		Environment: types.Mapping{},
	}, func(o *loader.Options) {
		o.SetProjectName(project, true)
		o.ResolvePaths = true
		o.Interpolate = &interpolation.Options{
			LookupValue: func(string) (string, bool) { return "", false },
			Substitute:  substitute,
		}
	})
	if err != nil {
		c.add("", "compose", "%v", err)
		return nil, c.err()
	}
	c.names = map[string]string{}
	holders := map[string]string{}
	for _, name := range p.ServiceNames() {
		svc, err := p.GetService(name)
		if err != nil {
			continue // named below
		}
		n := ContainerName(c.project, name)
		if svc.ContainerName != "" {
			n = svc.ContainerName
			if !spec.ValidName(n) {
				c.add(name, "container_name", "%q is not 1 to 63 of [a-z0-9_.-], starting with [a-z0-9]", n)
			}
		}
		if other, ok := holders[n]; ok {
			c.add(name, "container_name", "%s is also the container of service %s", n, other)
		}
		holders[n], c.names[name] = name, n
	}
	var specs []spec.Spec
	for _, name := range p.ServiceNames() {
		svc, err := p.GetService(name)
		if err != nil {
			c.add(name, "compose", "%v", err)
			continue
		}
		if s, ok := c.service(svc); ok {
			specs = append(specs, s)
		}
	}
	if len(c.problems) > 0 {
		return nil, c.err()
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

// substitute interpolates against no environment: a default applies, and a variable
// without one is refused.
func substitute(tmpl string, mapping template.Mapping) (string, error) {
	return template.SubstituteWithOptions(tmpl, mapping, template.WithoutLogging,
		template.WithReplacementFunction(func(s string, m template.Mapping, cfg *template.Config) (string, error) {
			v, applied, err := template.DefaultReplacementAppliedFunc(s, m, cfg)
			if err == nil && !applied {
				return "", fmt.Errorf("interpolation: %s is not set and has no default", s)
			}
			return v, err
		}))
}

type checker struct {
	project, dir, volumes string
	names                 map[string]string // service -> its container's name
	problems              []Problem
}

func (c *checker) add(service, field, format string, args ...any) {
	c.problems = append(c.problems, Problem{Service: service, Field: field, Detail: fmt.Sprintf(format, args...)})
}

func (c *checker) err() error {
	sort.SliceStable(c.problems, func(i, j int) bool { return c.problems[i].Service < c.problems[j].Service })
	return &Error{Problems: c.problems}
}

// only refuses every key of m that allowed lacks, by its path.
func (c *checker) only(service, at string, m map[string]any, allowed map[string]bool) {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !allowed[k] && !strings.HasPrefix(k, "x-") {
			field := k
			if at != "" {
				field = at + "." + k
			}
			c.add(service, field, "%s is not supported", field)
		}
	}
}

func mapOf(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// raw checks what is written, before compose-go fills its defaults in.
func (c *checker) raw(top map[string]any) {
	c.only("", "", top, topKeys)
	if n, ok := top["name"]; ok && n != c.project {
		c.add("", "name", "the file names project %v, not %s", n, c.project)
	}
	if nets, ok := mapOf(top["networks"]); ok {
		for name, v := range nets {
			if m, _ := mapOf(v); name != "default" || len(m) > 0 {
				c.add("", "networks", "network %s: only the project network default, without options, is supported", name)
			}
		}
	}
	if vols, ok := mapOf(top["volumes"]); ok {
		for name, v := range vols {
			if m, ok := mapOf(v); ok {
				c.only("", "volumes."+name, m, nil)
			}
		}
	}
	services, ok := mapOf(top["services"])
	if !ok || len(services) == 0 {
		c.add("", "services", "the file declares no service")
		return
	}
	for name, v := range services {
		if !serviceRE.MatchString(name) {
			c.add(name, "name", "service %q is not 1 to 20 of [a-z0-9_-], starting with a letter or digit", name)
			continue
		}
		svc, ok := mapOf(v)
		if !ok {
			continue // compose-go names the shape
		}
		c.only(name, "", svc, serviceKeys)
		c.rawService(name, svc)
	}
}

func (c *checker) rawService(name string, svc map[string]any) {
	if ports, ok := svc["ports"].([]any); ok {
		for _, p := range ports {
			if m, ok := mapOf(p); ok {
				c.only(name, "ports", m, portKeys)
				if mode, ok := m["mode"]; ok && mode != "ingress" {
					c.add(name, "ports.mode", "mode %v is not supported", mode)
				}
			}
		}
	}
	if vols, ok := svc["volumes"].([]any); ok {
		for _, v := range vols {
			source, _ := v.(string)
			source, _, _ = strings.Cut(source, ":")
			if m, ok := mapOf(v); ok {
				c.only(name, "volumes", m, volumeKeys)
				source, _ = m["source"].(string)
			}
			if strings.HasPrefix(source, "~") {
				c.add(name, "volumes", "%s: a ~ path is not supported; there is no home directory", source)
			}
		}
	}
	var envFiles []any
	switch e := svc["env_file"].(type) {
	case string:
		envFiles = []any{e}
	case []any:
		envFiles = e
	}
	for _, e := range envFiles {
		p, _ := e.(string)
		if m, ok := mapOf(e); ok {
			c.only(name, "env_file", m, envFileKeys)
			p, _ = m["path"].(string)
		}
		if strings.HasPrefix(p, "~") {
			c.add(name, "env_file", "%s: a ~ path is not supported; there is no home directory", p)
		}
	}
	if devices, ok := svc["devices"].([]any); ok {
		for _, d := range devices {
			if m, ok := mapOf(d); ok {
				c.only(name, "devices", m, deviceKeys)
			}
		}
	}
	if deps, ok := mapOf(svc["depends_on"]); ok {
		for dep, v := range deps {
			if m, ok := mapOf(v); ok {
				c.only(name, "depends_on."+dep, m, dependsKeys)
			}
		}
	}
	if h, ok := mapOf(svc["healthcheck"]); ok {
		c.only(name, "healthcheck", h, healthKeys)
	}
	if d, ok := mapOf(svc["deploy"]); ok {
		c.only(name, "deploy", d, deployKeys)
		if r, ok := mapOf(d["resources"]); ok {
			c.only(name, "deploy.resources", r, resourcesKeys)
			if l, ok := mapOf(r["limits"]); ok {
				c.only(name, "deploy.resources.limits", l, limitsKeys)
			}
		}
	}
	if _, ok := svc["network_mode"]; ok && svc["networks"] != nil {
		c.add(name, "networks", "networks is not supported beside network_mode")
	}
	switch n := svc["networks"].(type) {
	case []any:
		for _, v := range n {
			if v != "default" {
				c.add(name, "networks", "network %v: only the project network default is supported", v)
			}
		}
	case map[string]any:
		for k, v := range n {
			if m, _ := mapOf(v); k != "default" || len(m) > 0 {
				c.add(name, "networks", "network %s: only the project network default, without options, is supported", k)
			}
		}
	}
}

// service is one service as a spec, or false with its problems added.
func (c *checker) service(svc types.ServiceConfig) (spec.Spec, bool) {
	name := svc.Name
	before := len(c.problems)
	s := spec.Spec{
		Name: c.names[name], Project: c.project, Service: name,
		Image: svc.Image, Entrypoint: svc.Entrypoint, Command: svc.Command, User: svc.User, WorkingDir: svc.WorkingDir,
	}
	switch svc.NetworkMode {
	case "":
		s.Network, s.Aliases = NetworkName(c.project), []string{name}
	case spec.NetworkHost, spec.NetworkNone:
		s.NetworkMode = svc.NetworkMode
		if s.NetworkMode == spec.NetworkHost && len(svc.Ports) > 0 {
			c.add(name, "ports", "a service on the host network publishes no port: it listens on the host")
		}
	default:
		c.add(name, "network_mode", "%q is not host or none", svc.NetworkMode)
	}
	s.CapAdd = c.capabilities(name, "cap_add", svc.CapAdd)
	s.CapDrop = c.capabilities(name, "cap_drop", svc.CapDrop)
	for k, v := range svc.Environment {
		if v == nil {
			c.add(name, "environment", "%s has no value, and there is no environment to pass through", k)
			continue
		}
		if s.Environment == nil {
			s.Environment = map[string]string{}
		}
		s.Environment[k] = *v
	}
	for _, p := range svc.Ports {
		port, ok := c.port(name, p)
		if ok {
			s.Publish = append(s.Publish, port)
		}
	}
	for _, v := range svc.Volumes {
		vol, ok := c.volume(name, v)
		if ok {
			s.Volumes = append(s.Volumes, vol)
		}
	}
	for _, d := range svc.Devices {
		dev, ok := c.device(name, d)
		if ok {
			s.Devices = append(s.Devices, dev)
		}
	}
	s.DeviceCgroupRules = svc.DeviceCgroupRules
	c.restart(name, svc.Restart, &s.Restart)
	deps := make([]string, 0, len(svc.DependsOn))
	for d := range svc.DependsOn {
		deps = append(deps, d)
	}
	sort.Strings(deps)
	for _, d := range deps {
		dep := svc.DependsOn[d]
		ready := spec.ReadyRunning
		switch dep.Condition {
		case "", types.ServiceConditionStarted:
		case types.ServiceConditionHealthy:
			ready = spec.ReadyHealthy
		default:
			c.add(name, "depends_on", "%s: condition %s is not supported", d, dep.Condition)
		}
		if !dep.Required {
			c.add(name, "depends_on", "%s: required: false is not supported", d)
		}
		if dep.Restart {
			c.add(name, "depends_on", "%s: restart: true is not supported", d)
		}
		s.DependsOn = append(s.DependsOn, spec.Dependency{Name: c.names[d], Ready: ready})
	}
	s.Health = c.health(name, svc.HealthCheck)
	c.limits(name, svc, &s.Limits)
	if len(c.problems) > before {
		return s, false
	}
	if err := s.Validate(); err != nil {
		var se *spec.Error
		if errors.As(err, &se) {
			c.add(name, se.Rule, "%s", se.Detail)
		} else {
			c.add(name, "spec", "%v", err)
		}
		return s, false
	}
	return s, true
}

// capabilities are names as podman takes them, any case, with or without CAP_, or
// ALL; written as CAP_<NAME>.
func (c *checker) capabilities(service, field string, names []string) []string {
	var out []string
	for _, n := range names {
		up := strings.ToUpper(n)
		if up != spec.CapAll && !strings.HasPrefix(up, "CAP_") {
			up = "CAP_" + up
		}
		if up != spec.CapAll && !spec.Capabilities[up] {
			c.add(service, field, "%q is not a capability podman knows, or ALL", n)
			continue
		}
		out = append(out, up)
	}
	return out
}

func (c *checker) port(service string, p types.ServicePortConfig) (spec.Port, bool) {
	switch {
	case p.HostIP != "":
		c.add(service, "ports", "%d: a host address (%s) is not supported; a port is published on every address", p.Target, p.HostIP)
	case p.Mode == "host":
		c.add(service, "ports", "%d: mode host is not supported", p.Target)
	case p.Published == "":
		c.add(service, "ports", "%d: no published port; a port is published on the one named", p.Target)
	default:
		host, err := strconv.ParseUint(p.Published, 10, 16)
		if err != nil || p.Target > math.MaxUint16 {
			c.add(service, "ports", "%s:%d is not one host port and one container port", p.Published, p.Target)
			return spec.Port{}, false
		}
		return spec.Port{Host: uint16(host), Container: uint16(p.Target), Protocol: p.Protocol}, true
	}
	return spec.Port{}, false
}

func (c *checker) volume(service string, v types.ServiceVolumeConfig) (spec.Volume, bool) {
	switch v.Type {
	case types.VolumeTypeBind:
		host, existing, err := resolve(path.Clean(v.Source))
		if err != nil {
			c.add(service, "volumes", "%v", err)
			return spec.Volume{}, false
		}
		// The short form makes a missing host directory, as compose does.
		create := v.Bind != nil && bool(v.Bind.CreateHostPath)
		if host != existing {
			if !create {
				c.add(service, "volumes", "%s does not exist", v.Source)
				return spec.Volume{}, false
			}
			if err := creatable(existing); err != nil {
				c.add(service, "volumes", "%s does not exist and cannot be made: %v", v.Source, err)
				return spec.Volume{}, false
			}
		}
		return spec.Volume{Host: host, Container: v.Target, ReadOnly: v.ReadOnly, Make: create}, true
	case types.VolumeTypeVolume:
		if v.Source == "" {
			c.add(service, "volumes", "%s: an anonymous volume is not supported; name it", v.Target)
			return spec.Volume{}, false
		}
		if v.Volume != nil && (v.Volume.NoCopy || v.Volume.Subpath != "") {
			c.add(service, "volumes", "%s: volume options are not supported", v.Target)
			return spec.Volume{}, false
		}
		return spec.Volume{Host: path.Join(c.volumes, v.Source), Container: v.Target, ReadOnly: v.ReadOnly, Make: true}, true
	default:
		c.add(service, "volumes", "%s: type %s is not supported", v.Target, v.Type)
		return spec.Volume{}, false
	}
}

// resolve is p with every link resolved, and the longest part of it that exists: p
// itself when it exists.
func resolve(p string) (resolved, existing string, err error) {
	rest := ""
	for q := p; ; q = path.Dir(q) {
		r, err := filepath.EvalSymlinks(q)
		if err == nil {
			return path.Join(r, rest), r, nil
		}
		missing := errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
		if _, lerr := os.Lstat(q); lerr == nil || !missing || q == "/" {
			return "", "", fmt.Errorf("%s: %w", q, err) // a link to nothing, or unreadable
		}
		rest = path.Join(path.Base(q), rest)
	}
}

// creatable reports why a directory cannot be made in dir, which exists.
func creatable(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	const writable = 2 // W_OK
	if err := syscall.Access(dir, writable); err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	return nil
}

// device is a host device as a spec: its path must name a device node now, directly or
// through a link, which is resolved again at each start.
func (c *checker) device(service string, d types.DeviceMapping) (spec.Device, bool) {
	out := spec.Device{Host: d.Source, Container: d.Target, Permissions: d.Permissions}
	if out.Container == "" {
		out.Container = out.Host
	}
	if out.Permissions == "" {
		out.Permissions = "rwm"
	}
	if !path.IsAbs(d.Source) {
		c.add(service, "devices", "%q is not an absolute path", d.Source)
		return out, false
	}
	fi, err := os.Stat(d.Source)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.add(service, "devices", "device %s is absent", d.Source)
	case err != nil:
		c.add(service, "devices", "%v", err)
	case fi.Mode()&fs.ModeDevice == 0:
		c.add(service, "devices", "%s is not a device", d.Source)
	default:
		return out, true
	}
	return out, false
}

func (c *checker) restart(service, restart string, r *spec.Restart) {
	switch {
	case restart == "" || restart == "no":
		r.Policy = spec.PolicyNo
	case restart == "always" || restart == "unless-stopped":
		r.Policy = spec.PolicyAlways
	case restart == "on-failure":
		r.Policy = spec.PolicyOnFailure
	case strings.HasPrefix(restart, "on-failure:"):
		n, err := strconv.Atoi(strings.TrimPrefix(restart, "on-failure:"))
		if err != nil || n < 1 {
			c.add(service, "restart", "%q is not on-failure:<count of at least 1>", restart)
			return
		}
		r.Policy, r.MaxRestarts = spec.PolicyOnFailure, n
	default:
		c.add(service, "restart", "%q is not no, always, unless-stopped or on-failure[:N]", restart)
	}
}

func (c *checker) seconds(service, field string, d *types.Duration) int {
	if d == nil {
		return 0
	}
	t := time.Duration(*d)
	if t%time.Second != 0 || t < 0 {
		c.add(service, "healthcheck", "%s %s is not a whole number of seconds", field, t)
		return 0
	}
	return int(t / time.Second)
}

func (c *checker) health(service string, h *types.HealthCheckConfig) *spec.Health {
	if h == nil || h.Disable || len(h.Test) == 0 || h.Test[0] == "NONE" {
		return nil
	}
	if h.StartInterval != nil {
		c.add(service, "healthcheck", "start_interval is not supported")
	}
	out := &spec.Health{
		IntervalSeconds:    c.seconds(service, "interval", h.Interval),
		TimeoutSeconds:     c.seconds(service, "timeout", h.Timeout),
		StartPeriodSeconds: c.seconds(service, "start_period", h.StartPeriod),
	}
	if h.Interval != nil && out.IntervalSeconds == 0 || h.Timeout != nil && out.TimeoutSeconds == 0 {
		c.add(service, "healthcheck", "interval and timeout are at least 1s")
	}
	if h.Retries != nil {
		out.Retries = int(min(*h.Retries, 1000))
		if out.Retries == 0 {
			c.add(service, "healthcheck", "retries is at least 1")
		}
	}
	switch h.Test[0] {
	case "CMD":
		out.Command = h.Test[1:]
	case "CMD-SHELL":
		out.Command = []string{"/bin/sh", "-c", strings.Join(h.Test[1:], " ")}
	default:
		c.add(service, "healthcheck", "test %q is not CMD, CMD-SHELL or NONE", h.Test[0])
	}
	return out
}

func (c *checker) limits(service string, svc types.ServiceConfig, l *spec.Limits) {
	memory, cpus, pids := int64(svc.MemLimit), float64(svc.CPUS), svc.PidsLimit
	if d := svc.Deploy; d != nil {
		if d.Replicas != nil && *d.Replicas != 1 {
			c.add(service, "deploy", "replicas %d is not supported; a service is one container", *d.Replicas)
		}
		// compose-go refuses the two forms naming different values.
		if lim := d.Resources.Limits; lim != nil {
			memory, cpus, pids = max(memory, int64(lim.MemoryBytes)), max(cpus, float64(lim.NanoCPUs)), max(pids, lim.Pids)
		}
	}
	if pids < 0 { // compose's -1, unlimited
		c.add(service, "pids_limit", "%d is not a limit; leave it out for podman's", pids)
	}
	l.Pids = max(pids, 0)
	switch {
	case memory <= 0:
	case memory%(1<<30) == 0:
		l.Memory = fmt.Sprintf("%dg", memory>>30)
	case memory%(1<<20) == 0:
		l.Memory = fmt.Sprintf("%dm", memory>>20)
	case memory%(1<<10) == 0:
		l.Memory = fmt.Sprintf("%dk", memory>>10)
	default:
		c.add(service, "mem_limit", "%d bytes is not a whole number of KiB", memory)
	}
	if cpus > 0 {
		l.CPU = strings.TrimRight(strings.TrimRight(strconv.FormatFloat(cpus, 'f', 3, 32), "0"), ".")
	}
}

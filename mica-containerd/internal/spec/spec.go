// Package spec is one container a Compose project's service becomes: what it runs, on
// which project network, and how it is restarted. A spec is validated before it is
// stored, and its runtime part is hashed so a container is recreated exactly when that
// part changes.
package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The restart policies: what happens when a container stops on its own.
const (
	PolicyNo        = "no"         // leave it stopped
	PolicyOnFailure = "on-failure" // restart it when it exits non-zero
	PolicyAlways    = "always"     // restart it whenever it exits
)

// The network modes beside a project network.
const (
	NetworkHost = "host"
	NetworkNone = "none"
)

// Capabilities are the Linux capabilities podman knows, by their CAP_ names.
var Capabilities = keys("CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID", "CAP_KILL",
	"CAP_SETGID", "CAP_SETUID", "CAP_SETPCAP", "CAP_LINUX_IMMUTABLE", "CAP_NET_BIND_SERVICE", "CAP_NET_BROADCAST",
	"CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_IPC_LOCK", "CAP_IPC_OWNER", "CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_CHROOT",
	"CAP_SYS_PTRACE", "CAP_SYS_PACCT", "CAP_SYS_ADMIN", "CAP_SYS_BOOT", "CAP_SYS_NICE", "CAP_SYS_RESOURCE", "CAP_SYS_TIME",
	"CAP_SYS_TTY_CONFIG", "CAP_MKNOD", "CAP_LEASE", "CAP_AUDIT_WRITE", "CAP_AUDIT_CONTROL", "CAP_SETFCAP",
	"CAP_MAC_OVERRIDE", "CAP_MAC_ADMIN", "CAP_SYSLOG", "CAP_WAKE_ALARM", "CAP_BLOCK_SUSPEND", "CAP_AUDIT_READ",
	"CAP_PERFMON", "CAP_BPF", "CAP_CHECKPOINT_RESTORE")

// CapAll is every capability, as podman takes it.
const CapAll = "ALL"

func keys(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// The readiness a dependency is waited for.
const (
	ReadyRunning = "running"
	ReadyHealthy = "healthy"
)

// How a container is restarted. StartRetries bounds the attempts of a container that
// never reached running; it counts as running once it has run StartSeconds. The gap
// before attempt n is n*BackoffSeconds, at most BackoffMaxSeconds; a run longer than
// BackoffMaxSeconds resets the tallies.
const (
	StartRetries      = 3
	StartSeconds      = 1
	BackoffSeconds    = 1
	BackoffMaxSeconds = 60
)

// Defaults of the health check values a spec leaves at zero.
const (
	DefaultHealthIntervalSeconds = 30
	DefaultHealthTimeoutSeconds  = 10
	DefaultHealthRetries         = 3
)

// Spec is one declared container.
type Spec struct {
	Name string `json:"name"`
	// Project and Service name the Compose project and service the container is of.
	Project string `json:"project"`
	Service string `json:"service"`
	Image   string `json:"image"`
	// Entrypoint replaces the image's; the image's when empty.
	Entrypoint []string `json:"entrypoint,omitempty"`
	// User and WorkingDir replace the image's (podman's --user and --workdir); the
	// image's when empty.
	User        string            `json:"user,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Publish     []Port            `json:"publish,omitempty"`
	Volumes     []Volume          `json:"volumes,omitempty"`
	// Network is the project's podman network the container joins, reachable there by
	// its name and Aliases; empty with a NetworkMode.
	Network string   `json:"network,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
	// NetworkMode is podman's host or none network instead of the project's.
	NetworkMode string `json:"network_mode,omitempty"`
	// CapAdd and CapDrop are podman's --cap-add and --cap-drop: capability names as
	// CAP_<NAME>, or ALL.
	CapAdd  []string `json:"cap_add,omitempty"`
	CapDrop []string `json:"cap_drop,omitempty"`
	// Devices are host devices made available in the container (podman's --device);
	// DeviceCgroupRules are podman's --device-cgroup-rule.
	Devices           []Device `json:"devices,omitempty"`
	DeviceCgroupRules []string `json:"device_cgroup_rules,omitempty"`
	Limits            Limits   `json:"limits"`
	Restart           Restart  `json:"restart"`
	// Health is the container's health check; none when nil.
	Health *Health `json:"health,omitempty"`
	// DependsOn are the containers this one starts after.
	DependsOn []Dependency `json:"depends_on,omitempty"`
}

// Health is a command run in the container: exit 0 is a pass. The container is
// healthy after one pass and unhealthy after Retries failures in a row; failures in
// its first StartPeriodSeconds do not count.
type Health struct {
	Command            []string `json:"command"`
	IntervalSeconds    int      `json:"interval_seconds"`
	TimeoutSeconds     int      `json:"timeout_seconds"`
	Retries            int      `json:"retries"`
	StartPeriodSeconds int      `json:"start_period_seconds"`
}

// Dependency is a container waited for before this one starts.
type Dependency struct {
	Name  string `json:"name"`
	Ready string `json:"ready"`
}

// Port is one published port.
type Port struct {
	Host      uint16 `json:"host"`
	Container uint16 `json:"container"`
	Protocol  string `json:"protocol"`
}

// Volume is one host path mounted into the container. Make creates the host directory
// when it is missing.
type Volume struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	ReadOnly  bool   `json:"read_only"`
	Make      bool   `json:"make,omitempty"`
}

// Device is a host device node, or a link to one such as /dev/serial/by-id/..., at a
// path in the container, with the cgroup permissions of r, w and m.
type Device struct {
	Host        string `json:"host"`
	Container   string `json:"container"`
	Permissions string `json:"permissions"`
}

// Limits are the container's ceilings; zero or empty is podman's own default.
type Limits struct {
	Pids   int64  `json:"pids,omitempty"`
	Memory string `json:"memory,omitempty"`
	CPU    string `json:"cpu,omitempty"`
}

// Restart is how a container that stops on its own is brought back. MaxRestarts
// bounds the restarts of one that reached running and kept stopping (0 is unlimited).
type Restart struct {
	Policy      string `json:"policy"`
	MaxRestarts int    `json:"max_restarts"`
}

// Error is a spec refused, by the rule it breaks.
type Error struct {
	Rule   string
	Detail string
}

func (e *Error) Error() string { return e.Rule + ": " + e.Detail }

func refuse(rule, format string, args ...any) error {
	return &Error{Rule: rule, Detail: fmt.Sprintf(format, args...)}
}

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	userRE   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,31})?$`)
	memoryRE = regexp.MustCompile(`^[1-9][0-9]*[kmg]$`)
	cpuRE    = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,3})?$`)
	permRE   = regexp.MustCompile(`^[rwm]{1,3}$`)
	ruleRE   = regexp.MustCompile(`^[abc] (\*|[0-9]+):(\*|[0-9]+) [rwm]{1,3}$`)
)

// ValidName reports whether name can name a container.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Normalized fills the defaults a spec leaves out, so equal intents compare equal.
func (s Spec) Normalized() Spec {
	if s.Restart.Policy == "" {
		s.Restart.Policy = PolicyNo
	}
	publish := make([]Port, len(s.Publish))
	for i, p := range s.Publish {
		if p.Protocol == "" {
			p.Protocol = "tcp"
		}
		publish[i] = p
	}
	s.Publish = publish
	if s.Health != nil {
		h := *s.Health
		if h.IntervalSeconds == 0 {
			h.IntervalSeconds = DefaultHealthIntervalSeconds
		}
		if h.TimeoutSeconds == 0 {
			h.TimeoutSeconds = DefaultHealthTimeoutSeconds
		}
		if h.Retries == 0 {
			h.Retries = DefaultHealthRetries
		}
		s.Health = &h
	}
	deps := make([]Dependency, len(s.DependsOn))
	for i, d := range s.DependsOn {
		if d.Ready == "" {
			d.Ready = ReadyRunning
		}
		deps[i] = d
	}
	s.DependsOn = deps
	return s
}

// Validate refuses a spec podman could not run as declared, or could run as
// something else than declared (an argument its syntax would split).
func (s Spec) Validate() error {
	s = s.Normalized()
	if !nameRE.MatchString(s.Name) {
		return refuse("name", "%q is not 1 to 63 of [a-z0-9_.-], starting with [a-z0-9]", s.Name)
	}
	if s.Image == "" || len(s.Image) > 512 || strings.HasPrefix(s.Image, "-") || strings.ContainsAny(s.Image, " \t\n\x00") {
		return refuse("image", "%q is not an image reference", s.Image)
	}
	for _, arg := range s.Command {
		if strings.ContainsRune(arg, 0) {
			return refuse("command", "an argument carries a NUL byte")
		}
	}
	for _, arg := range s.Entrypoint {
		if strings.ContainsRune(arg, 0) {
			return refuse("entrypoint", "an argument carries a NUL byte")
		}
	}
	if s.User != "" && !userRE.MatchString(s.User) {
		return refuse("user", "%q is not a user, a uid, or either with :group or :gid", s.User)
	}
	if s.WorkingDir != "" && (!path.IsAbs(s.WorkingDir) || path.Clean(s.WorkingDir) != s.WorkingDir || strings.ContainsRune(s.WorkingDir, 0)) {
		return refuse("working_dir", "%q is not a clean absolute path", s.WorkingDir)
	}
	if !nameRE.MatchString(s.Project) || !nameRE.MatchString(s.Service) {
		return refuse("project", "%s: project %q and service %q are not names", s.Name, s.Project, s.Service)
	}
	switch s.NetworkMode {
	case "":
		if !nameRE.MatchString(s.Network) {
			return refuse("network", "%q is not 1 to 63 of [a-z0-9_.-], starting with [a-z0-9]", s.Network)
		}
		for _, a := range s.Aliases {
			if !nameRE.MatchString(a) {
				return refuse("network", "alias %q is not a name on a network", a)
			}
		}
	case NetworkHost, NetworkNone:
		if s.Network != "" || len(s.Aliases) > 0 {
			return refuse("network", "network mode %s joins no network and has no alias", s.NetworkMode)
		}
		if s.NetworkMode == NetworkHost && len(s.Publish) > 0 {
			return refuse("publish", "a container on the host network publishes no port: it listens on the host")
		}
	default:
		return refuse("network", "network mode %q is not host or none", s.NetworkMode)
	}
	for _, c := range append(append([]string{}, s.CapAdd...), s.CapDrop...) {
		if c != CapAll && !Capabilities[c] {
			return refuse("capabilities", "%q is not a capability (CAP_<NAME>) or ALL", c)
		}
	}
	for k, v := range s.Environment {
		if !envKeyRE.MatchString(k) {
			return refuse("environment", "%q is not a variable name", k)
		}
		if strings.ContainsRune(v, 0) {
			return refuse("environment", "the value of %s carries a NUL byte", k)
		}
	}
	seen := map[string]bool{}
	for _, p := range s.Publish {
		if p.Host == 0 || p.Container == 0 {
			return refuse("publish", "port 0 in %d:%d", p.Host, p.Container)
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return refuse("publish", "protocol %q is not tcp or udp", p.Protocol)
		}
		key := fmt.Sprintf("%d/%s", p.Host, p.Protocol)
		if seen[key] {
			return refuse("publish", "host port %s is published twice", key)
		}
		seen[key] = true
	}
	for _, v := range s.Volumes {
		for _, p := range []string{v.Host, v.Container} {
			if !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, ":,\x00") {
				return refuse("volume", "%q is not a clean absolute path without ':' or ','", p)
			}
		}
	}
	for _, d := range s.Devices {
		// The host path may be a link such as /dev/serial/by-path/pci-0000:00:14.0-...:
		// podman is given the node it names, never the link.
		if !path.IsAbs(d.Host) || path.Clean(d.Host) != d.Host || strings.Contains(d.Host, "\x00") {
			return refuse("devices", "%q is not a clean absolute path", d.Host)
		}
		if !path.IsAbs(d.Container) || path.Clean(d.Container) != d.Container || strings.ContainsAny(d.Container, ":,\x00") {
			return refuse("devices", "%q is not a clean absolute path without ':' or ','", d.Container)
		}
		if !permRE.MatchString(d.Permissions) {
			return refuse("devices", "%s: permissions %q are not of r, w and m", d.Host, d.Permissions)
		}
	}
	for _, r := range s.DeviceCgroupRules {
		if !ruleRE.MatchString(r) {
			return refuse("devices", "device cgroup rule %q is not <a|b|c> <major|*>:<minor|*> <rwm>", r)
		}
	}
	if s.Limits.Pids < 0 || s.Limits.Pids > 65536 {
		return refuse("limits", "pids %d is not 1 to 65536", s.Limits.Pids)
	}
	if s.Limits.Memory != "" && !memoryRE.MatchString(s.Limits.Memory) {
		return refuse("limits", "memory %q is not <n>k, <n>m or <n>g", s.Limits.Memory)
	}
	if s.Limits.CPU != "" && (!cpuRE.MatchString(s.Limits.CPU) || strings.Trim(s.Limits.CPU, "0.") == "") {
		return refuse("limits", "cpu %q is not a positive number of CPUs with at most three decimals", s.Limits.CPU)
	}
	switch s.Restart.Policy {
	case PolicyNo, PolicyOnFailure, PolicyAlways:
	default:
		return refuse("restart", "policy %q is not no, on-failure or always", s.Restart.Policy)
	}
	if s.Restart.MaxRestarts < 0 {
		return refuse("restart", "max_restarts %d is negative", s.Restart.MaxRestarts)
	}
	if h := s.Health; h != nil {
		if len(h.Command) == 0 || h.Command[0] == "" {
			return refuse("health", "the command is empty")
		}
		for _, arg := range h.Command {
			if strings.ContainsRune(arg, 0) {
				return refuse("health", "an argument carries a NUL byte")
			}
		}
		if h.IntervalSeconds < 1 || h.TimeoutSeconds < 1 || h.Retries < 1 || h.StartPeriodSeconds < 0 {
			return refuse("health", "interval, timeout and retries are at least 1, the start period at least 0")
		}
		if h.TimeoutSeconds > h.IntervalSeconds {
			return refuse("health", "timeout %ds exceeds the interval %ds", h.TimeoutSeconds, h.IntervalSeconds)
		}
	}
	seenDep := map[string]bool{}
	for _, d := range s.DependsOn {
		if !nameRE.MatchString(d.Name) || d.Name == s.Name || seenDep[d.Name] {
			return refuse("depends_on", "%q is not another container named once", d.Name)
		}
		seenDep[d.Name] = true
		if d.Ready != ReadyRunning && d.Ready != ReadyHealthy {
			return refuse("depends_on", "ready %q is not running or healthy", d.Ready)
		}
	}
	return nil
}

// runtime is the part of a spec the container is created from. The restart policy is
// the supervisor's, and changing it recreates nothing. Empty fields are omitted, so a
// spec keeps its hash when a field is added.
type runtime struct {
	Image       string            `json:"image"`
	Entrypoint  []string          `json:"entrypoint,omitempty"`
	User        string            `json:"user,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Publish     []Port            `json:"publish"`
	Volumes     []Volume          `json:"volumes"`
	Network     string            `json:"network,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
	NetworkMode string            `json:"network_mode,omitempty"`
	CapAdd      []string          `json:"cap_add,omitempty"`
	CapDrop     []string          `json:"cap_drop,omitempty"`
	Devices     []Device          `json:"devices,omitempty"`
	CgroupRules []string          `json:"device_cgroup_rules,omitempty"`
	Project     string            `json:"project,omitempty"`
	Limits      Limits            `json:"limits"`
	Health      *Health           `json:"health"`
}

// Hash is the sha256 of the runtime part of the normalized spec, as canonical JSON
// (encoding/json sorts map keys; list order is significant).
func (s Spec) Hash() string {
	n := s.Normalized()
	b, err := json.Marshal(runtime{n.Image, n.Entrypoint, n.User, n.WorkingDir, n.Command, n.Environment, n.Publish, n.Volumes, n.Network, n.Aliases, n.NetworkMode, n.CapAdd, n.CapDrop, n.Devices, n.DeviceCgroupRules, n.Project, n.Limits, n.Health})
	if err != nil {
		panic(err) // plain strings, numbers and maps: marshalling cannot fail
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

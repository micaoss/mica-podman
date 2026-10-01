// Package spec is what a client declares for one container: what it runs, and how it is
// restarted and started at boot. A spec is validated before it is stored, and its
// runtime part is hashed so a container is recreated exactly when that part changes.
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
	// PolicyOnUnhealthy restarts it when it exits non-zero or its health check fails.
	PolicyOnUnhealthy = "on-unhealthy"
)

// The readiness a dependency is waited for.
const (
	ReadyRunning = "running"
	ReadyHealthy = "healthy"
)

// Defaults of the restart knobs a spec leaves at zero.
const (
	DefaultStartRetries      = 3
	DefaultStartSeconds      = 1
	DefaultBackoffSeconds    = 1
	DefaultBackoffMaxSeconds = 60

	DefaultHealthIntervalSeconds = 30
	DefaultHealthTimeoutSeconds  = 10
	DefaultHealthRetries         = 3
)

// Spec is one declared container.
type Spec struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Publish     []Port            `json:"publish,omitempty"`
	Volumes     []Volume          `json:"volumes,omitempty"`
	Limits      Limits            `json:"limits"`
	Restart     Restart           `json:"restart"`
	// Health is the container's health check; none when nil.
	Health *Health `json:"health,omitempty"`
	// DependsOn are the containers this one starts after.
	DependsOn []Dependency `json:"depends_on,omitempty"`
	// Autostart starts the container at every boot.
	Autostart bool `json:"autostart"`
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

// Volume is one host path mounted into the container.
type Volume struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	ReadOnly  bool   `json:"read_only"`
}

// Limits are the container's ceilings; zero or empty is podman's own default.
type Limits struct {
	Pids   int64  `json:"pids,omitempty"`
	Memory string `json:"memory,omitempty"`
	CPU    string `json:"cpu,omitempty"`
}

// Restart is how a container that stops on its own is brought back.
//
// StartRetries bounds the attempts of a container that never reached running;
// MaxRestarts bounds the restarts of one that did and kept stopping (0 is
// unlimited). A container counts as running once it has run StartSeconds. The gap
// before attempt n is n*BackoffSeconds, at most BackoffMaxSeconds; a run longer than
// BackoffMaxSeconds resets both tallies.
type Restart struct {
	Policy            string `json:"policy"`
	StartRetries      int    `json:"start_retries"`
	MaxRestarts       int    `json:"max_restarts"`
	StartSeconds      int    `json:"start_seconds"`
	BackoffSeconds    int    `json:"backoff_seconds"`
	BackoffMaxSeconds int    `json:"backoff_max_seconds"`
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
	memoryRE = regexp.MustCompile(`^[1-9][0-9]*[kmg]$`)
	cpuRE    = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,3})?$`)
)

// ValidName reports whether name can name a container.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Normalized fills the defaults a spec leaves out, so equal intents compare equal.
func (s Spec) Normalized() Spec {
	if s.Restart.Policy == "" {
		s.Restart.Policy = PolicyNo
	}
	if s.Restart.StartRetries == 0 {
		s.Restart.StartRetries = DefaultStartRetries
	}
	if s.Restart.StartSeconds == 0 {
		s.Restart.StartSeconds = DefaultStartSeconds
	}
	if s.Restart.BackoffSeconds == 0 {
		s.Restart.BackoffSeconds = DefaultBackoffSeconds
	}
	if s.Restart.BackoffMaxSeconds == 0 {
		s.Restart.BackoffMaxSeconds = DefaultBackoffMaxSeconds
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
	if s.Limits.Pids < 0 || s.Limits.Pids > 65536 {
		return refuse("limits", "pids %d is not 1 to 65536", s.Limits.Pids)
	}
	if s.Limits.Memory != "" && !memoryRE.MatchString(s.Limits.Memory) {
		return refuse("limits", "memory %q is not <n>k, <n>m or <n>g", s.Limits.Memory)
	}
	if s.Limits.CPU != "" && (!cpuRE.MatchString(s.Limits.CPU) || strings.Trim(s.Limits.CPU, "0.") == "") {
		return refuse("limits", "cpu %q is not a positive number of CPUs with at most three decimals", s.Limits.CPU)
	}
	r := s.Restart
	switch r.Policy {
	case PolicyNo, PolicyOnFailure, PolicyAlways:
	case PolicyOnUnhealthy:
		if s.Health == nil {
			return refuse("restart", "policy on-unhealthy needs a health check")
		}
	default:
		return refuse("restart", "policy %q is not no, on-failure, always or on-unhealthy", r.Policy)
	}
	if r.StartRetries < 0 || r.MaxRestarts < 0 || r.StartSeconds < 0 || r.BackoffSeconds < 0 {
		return refuse("restart", "a count or a duration is negative")
	}
	if r.BackoffMaxSeconds < r.BackoffSeconds {
		return refuse("restart", "backoff_max_seconds %d is below backoff_seconds %d", r.BackoffMaxSeconds, r.BackoffSeconds)
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

// runtime is the part of a spec the container is created from. The restart knobs
// and autostart are the supervisor's, and changing them recreates nothing.
type runtime struct {
	Image       string            `json:"image"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Publish     []Port            `json:"publish"`
	Volumes     []Volume          `json:"volumes"`
	Limits      Limits            `json:"limits"`
	Health      *Health           `json:"health"`
}

// Hash is the sha256 of the runtime part of the normalized spec, as canonical JSON
// (encoding/json sorts map keys; list order is significant).
func (s Spec) Hash() string {
	n := s.Normalized()
	b, err := json.Marshal(runtime{n.Image, n.Command, n.Environment, n.Publish, n.Volumes, n.Limits, n.Health})
	if err != nil {
		panic(err) // plain strings, numbers and maps: marshalling cannot fail
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

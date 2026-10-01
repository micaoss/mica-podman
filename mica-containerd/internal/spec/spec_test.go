package spec

import (
	"errors"
	"testing"
)

func valid() Spec {
	return Spec{
		Name:        "web",
		Image:       "docker.io/library/nginx:1.29",
		Command:     []string{"nginx", "-g", "daemon off;"},
		Environment: map[string]string{"TZ": "UTC"},
		Publish:     []Port{{Host: 8080, Container: 80}},
		Volumes:     []Volume{{Host: "/mica/data/web", Container: "/usr/share/nginx/html", ReadOnly: true}},
		Limits:      Limits{Pids: 128, Memory: "64m", CPU: "0.5"},
		Restart:     Restart{Policy: PolicyOnFailure},
		Autostart:   true,
	}
}

func TestValidSpecPasses(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("a valid spec is refused: %v", err)
	}
}

func TestNormalizedFillsDefaults(t *testing.T) {
	n := Spec{Name: "a", Image: "i", Publish: []Port{{Host: 1, Container: 1}}}.Normalized()
	r := n.Restart
	if r.Policy != PolicyNo || r.StartRetries != DefaultStartRetries || r.StartSeconds != DefaultStartSeconds ||
		r.BackoffSeconds != DefaultBackoffSeconds || r.BackoffMaxSeconds != DefaultBackoffMaxSeconds {
		t.Fatalf("defaults not filled: %+v", r)
	}
	if n.Publish[0].Protocol != "tcp" {
		t.Fatalf("protocol default is %q", n.Publish[0].Protocol)
	}
}

func TestNormalizedDoesNotAliasTheCallersPorts(t *testing.T) {
	s := Spec{Publish: []Port{{Host: 1, Container: 1}}}
	s.Normalized()
	if s.Publish[0].Protocol != "" {
		t.Fatal("Normalized wrote into the caller's slice")
	}
}

func TestValidateRefusesByRule(t *testing.T) {
	cases := map[string]struct {
		edit func(*Spec)
		rule string
	}{
		"upper-case name":          {func(s *Spec) { s.Name = "Web" }, "name"},
		"empty name":               {func(s *Spec) { s.Name = "" }, "name"},
		"image with a space":       {func(s *Spec) { s.Image = "a b" }, "image"},
		"image read as an option":  {func(s *Spec) { s.Image = "--privileged" }, "image"},
		"NUL in the command":       {func(s *Spec) { s.Command = []string{"a\x00b"} }, "command"},
		"variable name with a dot": {func(s *Spec) { s.Environment = map[string]string{"A.B": "1"} }, "environment"},
		"port 0":                   {func(s *Spec) { s.Publish = []Port{{Host: 0, Container: 80}} }, "publish"},
		"sctp":                     {func(s *Spec) { s.Publish = []Port{{Host: 1, Container: 1, Protocol: "sctp"}} }, "publish"},
		"a host port twice": {func(s *Spec) {
			s.Publish = []Port{{Host: 1, Container: 1}, {Host: 1, Container: 2, Protocol: "tcp"}}
		}, "publish"},
		"relative host path":         {func(s *Spec) { s.Volumes = []Volume{{Host: "data", Container: "/d"}} }, "volume"},
		"a colon splits -v":          {func(s *Spec) { s.Volumes = []Volume{{Host: "/a:b", Container: "/d"}} }, "volume"},
		"an unclean path":            {func(s *Spec) { s.Volumes = []Volume{{Host: "/a/../b", Container: "/d"}} }, "volume"},
		"too many pids":              {func(s *Spec) { s.Limits.Pids = 65537 }, "limits"},
		"memory in bytes":            {func(s *Spec) { s.Limits.Memory = "1000" }, "limits"},
		"zero cpu":                   {func(s *Spec) { s.Limits.CPU = "0.000" }, "limits"},
		"four decimals of cpu":       {func(s *Spec) { s.Limits.CPU = "0.1234" }, "limits"},
		"unknown policy":             {func(s *Spec) { s.Restart.Policy = "unless-stopped" }, "restart"},
		"negative retries":           {func(s *Spec) { s.Restart.StartRetries = -1 }, "restart"},
		"cap below the first step":   {func(s *Spec) { s.Restart.BackoffSeconds = 10; s.Restart.BackoffMaxSeconds = 5 }, "restart"},
		"on-unhealthy with no check": {func(s *Spec) { s.Restart.Policy = PolicyOnUnhealthy }, "restart"},
		"an empty health command":    {func(s *Spec) { s.Health = &Health{} }, "health"},
		"a timeout over the interval": {func(s *Spec) {
			s.Health = &Health{Command: []string{"true"}, IntervalSeconds: 5, TimeoutSeconds: 6}
		}, "health"},
		"a dependency on itself": {func(s *Spec) { s.DependsOn = []Dependency{{Name: "web"}} }, "depends_on"},
		"a dependency twice":     {func(s *Spec) { s.DependsOn = []Dependency{{Name: "db"}, {Name: "db"}} }, "depends_on"},
		"an unknown readiness":   {func(s *Spec) { s.DependsOn = []Dependency{{Name: "db", Ready: "started"}} }, "depends_on"},
	}
	for label, c := range cases {
		s := valid()
		c.edit(&s)
		err := s.Validate()
		var e *Error
		if !errors.As(err, &e) || e.Rule != c.rule {
			t.Errorf("%s: want rule %q, got %v", label, c.rule, err)
		}
	}
}

func TestHashCoversTheRuntimePartOnly(t *testing.T) {
	a := valid()
	b := valid()
	b.Restart = Restart{Policy: PolicyAlways, MaxRestarts: 9}
	b.Autostart = false
	if a.Hash() != b.Hash() {
		t.Fatal("restart knobs or autostart changed the hash; they must not recreate the container")
	}
	c := valid()
	c.Environment = map[string]string{"TZ": "Asia/Shanghai"}
	if a.Hash() == c.Hash() {
		t.Fatal("an environment change left the hash")
	}
}

func TestHashIsStableUnderDefaults(t *testing.T) {
	a := valid()
	b := valid()
	b.Publish = []Port{{Host: 8080, Container: 80, Protocol: "tcp"}}
	if a.Hash() != b.Hash() {
		t.Fatal("an omitted protocol and its default hash differently")
	}
}

func TestHealthAndDependencyDefaults(t *testing.T) {
	s := valid()
	s.Health = &Health{Command: []string{"true"}}
	s.DependsOn = []Dependency{{Name: "db"}}
	s.Restart.Policy = PolicyOnUnhealthy
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	n := s.Normalized()
	if n.Health.IntervalSeconds != DefaultHealthIntervalSeconds || n.Health.Retries != DefaultHealthRetries || n.DependsOn[0].Ready != ReadyRunning {
		t.Fatalf("%+v %+v", n.Health, n.DependsOn)
	}
	if s.Health.IntervalSeconds != 0 {
		t.Fatal("Normalized wrote into the caller's health check")
	}
	h := valid()
	if h.Hash() == s.Hash() {
		t.Fatal("a health check left the hash: it is part of the container")
	}
}

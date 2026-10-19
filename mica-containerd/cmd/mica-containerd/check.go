package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/micaoss/mica-podman/mica-containerd/internal/compose"
	"github.com/micaoss/mica-podman/mica-containerd/internal/spec"
)

const checkUsage = `usage: mica-containerd compose check [-p NAME] [-f FILE | FILE] [--project-dir DIR] [--volumes DIR]

  FILE is a Compose file, - for stdin; without one, the first of compose.yaml,
  compose.yml, docker-compose.yaml and docker-compose.yml in the working directory.
  The project directory, of relative host paths, is FILE's directory; for stdin,
  the volumes directory/NAME, as the daemon takes a project declared without
  one. NAME defaults to the file's name key, else the name of FILE's directory
  or the working directory. Exit 0 and the containers it becomes on stdout, 1 and the
  refusal on stderr, 2 on a usage error.
`

// composeCheck holds a Compose file to the daemon's rules, with no daemon: the body
// on stdout or stderr is what the API answers.
func composeCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compose check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, checkUsage) }
	var pf projectFlags
	pf.add(fs)
	volumes := fs.String("volumes", defaultVolumes, "the directory of named volumes")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 || fs.NArg() == 1 && pf.file != "" {
		fs.Usage()
		return 2
	}
	if fs.NArg() == 1 {
		pf.file = fs.Arg(0)
	}
	t, err := pf.resolve(stdin, false)
	if err != nil {
		fmt.Fprintln(stderr, "compose check:", err)
		return 2
	}
	if pf.file == "-" && pf.dir == "" {
		// As the daemon takes a project declared without a project directory.
		t.dir = compose.Dir(*volumes, t.name)
	}
	specs, err := compose.Load(context.Background(), t.name, t.file, t.dir, *volumes)
	if err != nil {
		var ce *compose.Error
		body := map[string]any{"error": "compose", "detail": err.Error(), "problems": []compose.Problem{}}
		if errors.As(err, &ce) {
			body["problems"] = ce.Problems
		}
		write(stderr, body)
		return 1
	}
	if specs == nil {
		specs = []spec.Spec{}
	}
	write(stdout, map[string]any{"project": t.name, "projectDir": t.dir, "containers": specs})
	return 0
}

func write(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v) //nolint:errcheck // nothing to tell a closed stream
}

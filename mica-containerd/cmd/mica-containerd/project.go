package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/loader"
	"go.yaml.in/yaml/v4"
)

// composeFiles are the names a Compose file is found by in the working directory, in
// order, as docker compose finds one.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// projectFlags are -p, -f and --project-dir, as docker compose takes them.
type projectFlags struct{ project, file, dir string }

func (f *projectFlags) add(fs *flag.FlagSet) {
	fs.StringVar(&f.project, "p", "", "the project name")
	fs.StringVar(&f.project, "project", "", "the project name")
	fs.StringVar(&f.file, "f", "", "the Compose file, - for stdin")
	fs.StringVar(&f.file, "file", "", "the Compose file, - for stdin")
	fs.StringVar(&f.dir, "project-dir", "", "the project directory")
}

// target is a project as docker compose finds it: its file, read; its directory,
// the file's own, or the working directory for stdin; its name, the file's name key,
// or the directory's name made a project name.
type target struct {
	name, dir string
	file      []byte
}

// resolve finds the target. Without -f the file is looked up in the working
// directory; when optional, finding none is no error, and the target has no file.
func (f projectFlags) resolve(stdin io.Reader, optional bool) (target, error) {
	var t target
	cwd, err := os.Getwd()
	if err != nil {
		return t, err
	}
	file := f.file
	if file == "" {
		for _, n := range composeFiles {
			if _, err := os.Stat(filepath.Join(cwd, n)); err == nil {
				file = filepath.Join(cwd, n)
				break
			}
		}
	}
	switch {
	case file == "" && !optional:
		return t, fmt.Errorf("no Compose file: none of %v is in %s; name one with -f", composeFiles, cwd)
	case file == "":
		t.dir = cwd
	case file == "-":
		t.dir = cwd
		if t.file, err = io.ReadAll(stdin); err != nil {
			return t, err
		}
	default:
		abs, err := filepath.Abs(file)
		if err != nil {
			return t, err
		}
		t.dir = filepath.Dir(abs)
		if t.file, err = os.ReadFile(abs); err != nil { //nolint:gosec // a path the caller gave
			return t, err
		}
	}
	if f.dir != "" {
		if t.dir, err = filepath.Abs(f.dir); err != nil {
			return t, err
		}
		fi, err := os.Stat(t.dir)
		if err != nil {
			return t, err
		}
		if !fi.IsDir() {
			return t, fmt.Errorf("%s is not a directory", t.dir)
		}
	}
	t.name = f.project
	if t.name == "" {
		var top struct {
			Name any `yaml:"name"`
		}
		_ = yaml.Unmarshal(t.file, &top) //nolint:errcheck // a file that does not parse is refused when checked
		if n, ok := top.Name.(string); ok {
			t.name = n
		} else {
			t.name = loader.NormalizeProjectName(filepath.Base(t.dir))
		}
	}
	if t.name == "" {
		return t, fmt.Errorf("%s names no project; name one with -p", t.dir)
	}
	return t, nil
}

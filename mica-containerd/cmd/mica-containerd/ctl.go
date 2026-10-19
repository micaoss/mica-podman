package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
)

const ctlUsage = `usage: mica-containerd ctl [--socket PATH] <command>

  status                      the daemon
  projects                    every project, with its containers
  project PROJECT             one, with its Compose file, as JSON
  up [-p PROJECT] [-f FILE] [--project-dir DIR]
                              declare a project from its Compose file, found and
                              named as docker compose does (- is stdin)
  down [-p PROJECT]           remove a project, its containers and its network
  apply FILE                  declare exactly the projects of {"projects": {"<name>":
                              "<compose>" or {"compose": ..., "projectDir": ...}}} (- is stdin)
  list                        every container
  get NAME                    one container, as JSON
  start|stop|restart NAME     a container
  logs [--tail N] [--follow] NAME
  events                      phase changes as they happen
`

// ctl is the client: 0 on success, 1 when the API refused, 2 on a usage error.
func ctl(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", defaultSocket, "the API socket")
	fs.Usage = func() { fmt.Fprint(stderr, ctlUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c := &client{socket: *socket, stdout: stdout, stderr: stderr}
	a := fs.Args()
	if len(a) == 0 {
		fs.Usage()
		return 2
	}
	name := func(n int) (string, bool) {
		if len(a) != n+1 {
			fs.Usage()
			return "", false
		}
		return url.PathEscape(a[1]), true
	}
	switch a[0] {
	case "status":
		return c.do("GET", "/v1/status", nil, c.printJSON)
	case "projects":
		return c.do("GET", "/v1/projects", nil, c.printProjects)
	case "project":
		if n, ok := name(1); ok {
			return c.do("GET", "/v1/projects/"+n, nil, c.printJSON)
		}
	case "list":
		return c.do("GET", "/v1/containers", nil, c.printTable)
	case "get":
		if n, ok := name(1); ok {
			return c.do("GET", "/v1/containers/"+n, nil, c.printJSON)
		}
	case "up", "down":
		pfs := flag.NewFlagSet(a[0], flag.ContinueOnError)
		pfs.SetOutput(stderr)
		var pf projectFlags
		pf.add(pfs)
		if err := pfs.Parse(a[1:]); err != nil || pfs.NArg() != 0 || a[0] == "down" && (pf.file != "" || pf.dir != "") {
			fs.Usage()
			return 2
		}
		t, err := pf.resolve(stdin, a[0] == "down")
		if err != nil {
			fmt.Fprintln(stderr, "mica-containerd ctl:", err)
			return 2
		}
		if a[0] == "down" {
			return c.do("DELETE", "/v1/projects/"+url.PathEscape(t.name), nil, c.printJSON)
		}
		q := url.Values{"projectDir": {t.dir}}
		return c.do("PUT", "/v1/projects/"+url.PathEscape(t.name)+"?"+q.Encode(), bytes.NewReader(t.file), c.printJSON)
	case "start", "stop", "restart":
		if n, ok := name(1); ok {
			return c.do("POST", "/v1/containers/"+n+"/"+a[0], nil, c.printJSON)
		}
	case "apply":
		if len(a) == 2 {
			body, err := readFile(a[1])
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return c.do("PUT", "/v1/projects", body, c.printProjects)
		}
		fs.Usage()
	case "logs":
		lf := flag.NewFlagSet("logs", flag.ContinueOnError)
		lf.SetOutput(stderr)
		tail := lf.Int("tail", -1, "the last N lines")
		follow := lf.Bool("follow", false, "keep streaming")
		if err := lf.Parse(a[1:]); err != nil || lf.NArg() != 1 {
			fs.Usage()
			return 2
		}
		q := url.Values{}
		if *tail >= 0 {
			q.Set("tail", fmt.Sprint(*tail))
		}
		if *follow {
			q.Set("follow", "1")
		}
		return c.do("GET", "/v1/containers/"+url.PathEscape(lf.Arg(0))+"/logs?"+q.Encode(), nil, c.copy)
	case "events":
		return c.do("GET", "/v1/events", nil, c.copy)
	default:
		fs.Usage()
	}
	return 2
}

func readFile(path string) (io.Reader, error) {
	if path == "-" {
		return os.Stdin, nil
	}
	f, err := os.Open(path) //nolint:gosec // a path the person on the device gave
	if err != nil {
		return nil, err
	}
	return f, nil
}

type client struct {
	socket         string
	stdout, stderr io.Writer
}

func (c *client) do(method, path string, body io.Reader, show func(io.Reader) error) int {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
	}}}
	req, err := http.NewRequest(method, "http://mica-containerd"+path, body)
	if err != nil {
		fmt.Fprintln(c.stderr, err)
		return 2
	}
	resp, err := hc.Do(req)
	if err != nil {
		fmt.Fprintln(c.stderr, "mica-containerd ctl:", err)
		return 1
	}
	defer resp.Body.Close() //nolint:errcheck // read to the end or abandoned
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body) //nolint:errcheck // shown as far as it came
		fmt.Fprint(c.stderr, string(b))
		return 1
	}
	if resp.StatusCode == http.StatusNoContent {
		return 0
	}
	if err := show(resp.Body); err != nil {
		fmt.Fprintln(c.stderr, err)
		return 1
	}
	return 0
}

func (c *client) copy(r io.Reader) error {
	_, err := io.Copy(c.stdout, r)
	return err
}

func (c *client) printJSON(r io.Reader) error { return c.copy(r) }

// container is the part of a container entry the tables show.
type container struct {
	Project string `json:"project"`
	Spec    struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	} `json:"spec"`
	Desired  string `json:"desired"`
	Phase    string `json:"phase"`
	Restarts int    `json:"restarts"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error"`
}

func (c *client) table(containers []container) error {
	w := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROJECT\tDESIRED\tPHASE\tRESTARTS\tEXIT\tIMAGE\tERROR")
	for _, s := range containers {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n", s.Spec.Name, s.Project, s.Desired, s.Phase, s.Restarts, s.ExitCode, s.Spec.Image,
			strings.ReplaceAll(s.Error, "\n", " "))
	}
	return w.Flush()
}

func (c *client) printTable(r io.Reader) error {
	var body struct {
		Containers []container `json:"containers"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return err
	}
	return c.table(body.Containers)
}

func (c *client) printProjects(r io.Reader) error {
	var body struct {
		Projects []struct {
			Services []container `json:"services"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return err
	}
	var all []container
	for _, p := range body.Projects {
		all = append(all, p.Services...)
	}
	return c.table(all)
}

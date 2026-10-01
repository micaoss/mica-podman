package main

import (
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
  list                        every declared container
  get NAME                    one, as JSON
  put NAME FILE               declare NAME from a JSON spec (- is stdin)
  apply FILE                  declare exactly the containers of {"containers": [...]} (- is stdin)
  delete NAME                 remove NAME and its container
  start|stop|restart NAME
  logs [--tail N] [--follow] NAME
  events                      phase changes as they happen
`

// ctl is the client: 0 on success, 1 when the API refused, 2 on a usage error.
func ctl(args []string, stdout, stderr io.Writer) int {
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
	case "list":
		return c.do("GET", "/v1/containers", nil, c.printTable)
	case "get":
		if n, ok := name(1); ok {
			return c.do("GET", "/v1/containers/"+n, nil, c.printJSON)
		}
	case "delete":
		if n, ok := name(1); ok {
			return c.do("DELETE", "/v1/containers/"+n, nil, c.printJSON)
		}
	case "start", "stop", "restart":
		if n, ok := name(1); ok {
			return c.do("POST", "/v1/containers/"+n+"/"+a[0], nil, c.printJSON)
		}
	case "put":
		if n, ok := name(2); ok {
			body, err := readFile(a[2])
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return c.do("PUT", "/v1/containers/"+n, body, c.printJSON)
		}
	case "apply":
		if len(a) == 2 {
			body, err := readFile(a[1])
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return c.do("PUT", "/v1/containers", body, c.printTable)
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

func (c *client) printTable(r io.Reader) error {
	var body struct {
		Containers []struct {
			Spec struct {
				Name  string `json:"name"`
				Image string `json:"image"`
			} `json:"spec"`
			Desired  string `json:"desired"`
			Phase    string `json:"phase"`
			Restarts int    `json:"restarts"`
			ExitCode int    `json:"exit_code"`
			Error    string `json:"error"`
		} `json:"containers"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tDESIRED\tPHASE\tRESTARTS\tEXIT\tIMAGE\tERROR")
	for _, s := range body.Containers {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n", s.Spec.Name, s.Desired, s.Phase, s.Restarts, s.ExitCode, s.Spec.Image,
			strings.ReplaceAll(s.Error, "\n", " "))
	}
	return w.Flush()
}

// Command awxbench measures an AWX-compatible API (awxnext or AWX), with
// the same steps on both:
//
//	awxbench run --url https://awx.example.com --user admin --password ... --out awx.json
//	awxbench table awx=awx.json awxnext=awxnext.json
//
// The jobs run bench/playbooks from --project (this repository by default).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/bench"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		run(os.Args[2:])
	case "table":
		table(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: awxbench run --url URL --user U --password P [--out FILE] | awxbench table NAME=FILE...")
	os.Exit(2)
}

func run(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var t bench.Target
	o := bench.Options{}
	fs.StringVar(&t.URL, "url", "", "the API's base URL")
	fs.StringVar(&t.User, "user", "admin", "user")
	fs.StringVar(&t.Password, "password", os.Getenv("AWXBENCH_PASSWORD"), "password (or AWXBENCH_PASSWORD)")
	fs.BoolVar(&t.Insecure, "insecure", false, "skip TLS verification")
	fs.IntVar(&o.Hosts, "hosts", 10000, "hosts in the listed inventory")
	fs.IntVar(&o.ListRequests, "list-requests", 50, "requests per list measured")
	fs.IntVar(&o.Events, "events", 5000, "events of the event job")
	fs.IntVar(&o.Launches, "launches", 50, "small jobs launched")
	fs.IntVar(&o.ConcurrentLaunches, "concurrency", 10, "launches at a time")
	fs.StringVar(&o.ProjectURL, "project", "https://github.com/damhau/awxnext", "git repository holding bench/playbooks")
	fs.StringVar(&o.ProjectBranch, "branch", "main", "its branch")
	fs.BoolVar(&o.ContainerGroup, "container-group", false, "run the jobs in a container group (pods)")
	timeout := fs.Duration("timeout", 20*time.Minute, "longest wait for a job")
	out := fs.String("out", "", "write the result as JSON to this file")
	fs.Parse(args)
	if t.URL == "" {
		usage()
	}
	o.Timeout = *timeout
	o.Log = func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+format+"\n", a...)
	}
	r, err := bench.Run(context.Background(), t, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "awxbench:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	if *out != "" {
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "awxbench:", err)
			os.Exit(1)
		}
	}
	fmt.Println(string(b))
}

func table(args []string) {
	var names []string
	var results []*bench.Result
	for _, a := range args {
		name, file, ok := strings.Cut(a, "=")
		if !ok {
			usage()
		}
		b, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "awxbench:", err)
			os.Exit(1)
		}
		var r bench.Result
		if err := json.Unmarshal(b, &r); err != nil {
			fmt.Fprintln(os.Stderr, "awxbench:", file, err)
			os.Exit(1)
		}
		names, results = append(names, name), append(results, &r)
	}
	fmt.Print(bench.Markdown(results, names))
}

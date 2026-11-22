// Command awxbench measures an AWX-compatible API (awxnext or AWX), with
// the same steps on both:
//
//	awxbench run --url https://awx.example.com --user admin --password ... --out awx.json
//	awxbench table awx=awx.json awxnext=awxnext.json
//	awxbench rules --url https://awxnext.example.com --events 20000
//	awxbench masking --url https://awxnext.example.com --events 100000 --secrets 20
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
	case "rules":
		rulesRun(os.Args[2:])
	case "masking":
		maskingRun(os.Args[2:])
	case "reports":
		reportsRun(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: awxbench run --url URL --user U --password P [--out FILE] | awxbench table NAME=FILE... | awxbench rules --url URL [--events N] | awxbench masking --url URL [--events N] [--secrets N] | awxbench reports --url URL --database-url DSN [--runs N] [--clean]")
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

// rulesRun measures awxnext's event rules (AWX has none).
func rulesRun(args []string) {
	fs := flag.NewFlagSet("rules", flag.ExitOnError)
	var t bench.Target
	o := bench.RulesOptions{}
	fs.StringVar(&t.URL, "url", "", "awxnext's base URL")
	fs.StringVar(&t.User, "user", "admin", "user")
	fs.StringVar(&t.Password, "password", os.Getenv("AWXBENCH_PASSWORD"), "password (or AWXBENCH_PASSWORD)")
	fs.BoolVar(&t.Insecure, "insecure", false, "skip TLS verification")
	fs.IntVar(&o.Events, "events", 20000, "events posted")
	fs.IntVar(&o.Batch, "batch", 100, "events per request")
	fs.IntVar(&o.Concurrency, "concurrency", 8, "requests at a time")
	fs.IntVar(&o.Keys, "keys", 100, "hosts the events are about: the rule acts once per host")
	fs.DurationVar(&o.Timeout, "timeout", 10*time.Minute, "longest wait for the rule")
	fs.Parse(args)
	if t.URL == "" {
		usage()
	}
	o.Log = func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+format+"\n", a...)
	}
	r, err := bench.Rules(context.Background(), t, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "awxbench:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
}

// maskingRun measures what masking a job's secrets in its output costs.
func maskingRun(args []string) {
	fs := flag.NewFlagSet("masking", flag.ExitOnError)
	var t bench.Target
	o := bench.Options{}
	fs.StringVar(&t.URL, "url", "", "the API's base URL")
	fs.StringVar(&t.User, "user", "admin", "user")
	fs.StringVar(&t.Password, "password", os.Getenv("AWXBENCH_PASSWORD"), "password (or AWXBENCH_PASSWORD)")
	fs.BoolVar(&t.Insecure, "insecure", false, "skip TLS verification")
	fs.IntVar(&o.Events, "events", 100000, "events of each event job")
	secrets := fs.Int("secrets", 20, "secrets of the credential the second job uses")
	fs.StringVar(&o.ProjectURL, "project", "https://github.com/damhau/awxnext", "git repository holding bench/playbooks")
	fs.StringVar(&o.ProjectBranch, "branch", "main", "its branch")
	fs.BoolVar(&o.ContainerGroup, "container-group", false, "run the jobs in a container group (pods)")
	fs.DurationVar(&o.Timeout, "timeout", 30*time.Minute, "longest wait for a job")
	fs.Parse(args)
	if t.URL == "" {
		usage()
	}
	o.Log = func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+format+"\n", a...)
	}
	r, err := bench.Masking(context.Background(), t, o, *secrets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "awxbench:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
}

// reportsRun measures awxnext's reports over a large report history.
func reportsRun(args []string) {
	fs := flag.NewFlagSet("reports", flag.ExitOnError)
	var t bench.Target
	o := bench.ReportsOptions{}
	fs.StringVar(&t.URL, "url", "", "awxnext's base URL")
	fs.StringVar(&t.User, "user", "admin", "user")
	fs.StringVar(&t.Password, "password", os.Getenv("AWXBENCH_PASSWORD"), "password (or AWXBENCH_PASSWORD)")
	fs.BoolVar(&t.Insecure, "insecure", false, "skip TLS verification")
	fs.StringVar(&o.DatabaseURL, "database-url", os.Getenv("AWXNEXT_DATABASE_URL"), "awxnext's PostgreSQL, where the history is seeded (or AWXNEXT_DATABASE_URL)")
	fs.IntVar(&o.Runs, "runs", 1000000, "runs seeded")
	fs.IntVar(&o.Days, "days", 400, "days they cover")
	fs.IntVar(&o.Templates, "templates", 500, "templates they are of")
	fs.IntVar(&o.Hosts, "hosts", 5000, "hosts seeded")
	fs.IntVar(&o.HostTemplates, "host-templates", 3, "templates that run on each host every day")
	fs.IntVar(&o.Requests, "requests", 10, "requests per report, period and user")
	clean := fs.Bool("clean", false, "delete what an earlier run seeded, and stop")
	fs.Parse(args)
	if o.DatabaseURL == "" || (t.URL == "" && !*clean) {
		usage()
	}
	if *clean {
		n, err := bench.CleanReports(context.Background(), o.DatabaseURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "awxbench:", err)
			os.Exit(1)
		}
		fmt.Printf("deleted %d rows\n", n)
		return
	}
	o.Log = func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+format+"\n", a...)
	}
	r, err := bench.Reports(context.Background(), t, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "awxbench:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
}

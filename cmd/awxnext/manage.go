package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/server"
)

// manageCommands are awx-manage's instance and instance group commands,
// under their names with dashes or, as in AWX, underscores.
var manageCommands = map[string]bool{"provision-instance": true, "deprovision-instance": true, "register-queue": true,
	"unregister-queue": true, "remove-from-queue": true, "list-instances": true}

func manageCommand(name string) (string, bool) {
	n := strings.ReplaceAll(name, "_", "-")
	return n, manageCommands[n]
}

func manage(ctx context.Context, cfg *config.Config, name string, args []string) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	hostname := fs.String("hostname", "", "instance hostname")
	queue := fs.String("queuename", "", "instance group (queue) name")
	var nodeType, uuid, hostnames, podSpec *string
	var percent, minimum, forks, jobs *int
	var container, noColor *bool
	switch name {
	case "provision-instance":
		nodeType = fs.String("node_type", "hybrid", "control, execution, hop or hybrid")
		uuid = fs.String("uuid", "", "instance UUID")
	case "register-queue":
		hostnames = fs.String("hostnames", "", "comma-separated instances to list in the group (none is removed)")
		percent = fs.Int("instance_percent", 0, "percentage of instances the group takes")
		minimum = fs.Int("instance_minimum", 0, "minimum number of instances the group keeps")
		container = fs.Bool("is_container_group", false, "make it a container group")
		podSpec = fs.String("pod_spec_override", "", "container group pod spec (YAML)")
		forks = fs.Int("max_forks", 0, "maximum forks across the group's jobs")
		jobs = fs.Int("max_concurrent_jobs", 0, "maximum concurrent jobs on the group")
	case "list-instances":
		noColor = fs.Bool("no-color", false, "no ANSI colors")
		fs.BoolVar(noColor, "no_color", false, "no ANSI colors")
	}
	fs.Parse(args)
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	out := os.Stdout
	switch name {
	case "provision-instance":
		err = resources.ProvisionInstance(ctx, app, out, *hostname, *nodeType, *uuid)
	case "deprovision-instance":
		err = resources.DeprovisionInstance(ctx, app, out, *hostname)
	case "register-queue":
		var list []string
		if *hostnames != "" {
			list = strings.Split(*hostnames, ",")
		}
		err = resources.RegisterQueue(ctx, app, out, resources.Queue{Name: *queue, Hostnames: list, Percent: *percent, Minimum: *minimum,
			ContainerGroup: *container, PodSpecOverride: *podSpec, MaxForks: *forks, MaxConcurrent: *jobs})
	case "unregister-queue":
		err = resources.UnregisterQueue(ctx, app, out, *queue)
	case "remove-from-queue":
		err = resources.RemoveFromQueue(ctx, app, out, *queue, *hostname)
	case "list-instances":
		err = resources.ListInstances(ctx, app, out, !*noColor)
	}
	var failed *resources.CommandFailed
	if errors.As(err, &failed) {
		if !failed.Printed {
			fmt.Fprintln(os.Stderr, "CommandError: "+failed.Message)
		}
		os.Exit(1)
	}
	return err
}

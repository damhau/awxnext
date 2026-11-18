package jobs

import (
	"fmt"
	"maps"
	"strings"
)

// HideOutputs hides what the playbook publishes with set_stats under these
// names (a template's secret outputs): the values are replaced in the
// set_stats task's events and in the stats event, and masked in every
// event after them, as Mask does.
func (o *Output) HideOutputs(names ...string) {
	if len(names) == 0 {
		return
	}
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	o.hidden.Store(&set)
}

func (o *Output) hideOutputs(e Event) Event {
	names := o.hidden.Load()
	if names == nil || e.EventData == nil || !(strings.HasPrefix(e.Event, "runner_") || e.Event == "playbook_on_stats") {
		return e
	}
	var found []string
	data := hideStats(e.EventData, *names, &found)
	if len(found) == 0 {
		return e
	}
	o.Mask(found...)
	e.EventData = data.(map[string]any)
	return e
}

// hideStats copies v with the named values of set_stats data replaced:
// a task's result (ansible_stats.data, in each loop item's result too) and
// the stats event's artifact_data and per-host custom stats.
func hideStats(v any, names map[string]bool, found *[]string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, y := range x {
			switch k {
			case "ansible_stats":
				if m, ok := y.(map[string]any); ok {
					m = maps.Clone(m)
					m["data"] = hideNamed(m["data"], names, found)
					y = m
				}
			case "artifact_data":
				y = hideNamed(y, names, found)
			case "custom":
				if hosts, ok := y.(map[string]any); ok {
					c := make(map[string]any, len(hosts))
					for h, d := range hosts {
						c[h] = hideNamed(d, names, found)
					}
					y = c
				}
			default:
				y = hideStats(y, names, found)
			}
			out[k] = y
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = hideStats(y, names, found)
		}
		return out
	}
	return v
}

func hideNamed(v any, names map[string]bool, found *[]string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := maps.Clone(m)
	for k, y := range m {
		if !names[k] || y == nil {
			continue
		}
		if s, ok := y.(string); ok {
			*found = append(*found, s)
		} else {
			*found = append(*found, fmt.Sprint(y))
		}
		out[k] = Masked
	}
	return out
}

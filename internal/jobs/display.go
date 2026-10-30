package jobs

import "strings"

// eventDisplays are AWX's names of job events.
var eventDisplays = map[string]string{
	"playbook_on_start": "Playbook Started", "playbook_on_play_start": "Play Started", "playbook_on_task_start": "Task Started",
	"playbook_on_stats": "Playbook Complete", "runner_on_ok": "Host OK", "runner_on_failed": "Host Failed",
	"runner_on_skipped": "Host Skipped", "runner_on_unreachable": "Host Unreachable", "runner_on_start": "Host Started",
	"runner_item_on_ok": "Item OK", "runner_item_on_failed": "Item Failed", "runner_item_on_skipped": "Item Skipped",
	"runner_retry": "Host Retry", "runner_on_async_ok": "Host Async OK", "runner_on_async_failed": "Host Async Failure",
	"playbook_on_include": "Including File", "playbook_on_no_hosts_matched": "No Hosts Matched",
	"playbook_on_no_hosts_remaining": "No Hosts Remaining", "playbook_on_handler_task_start": "Handler Started",
	"playbook_on_notify": "Host Notified", "verbose": "Verbose", "debug": "Debug", "warning": "Warning", "error": "Error",
	"system_warning": "System Warning", "deprecated": "Deprecated",
}

// EventDisplay is a job event's event_display.
func EventDisplay(e string) string {
	if d, ok := eventDisplays[e]; ok {
		return d
	}
	parts := strings.Split(e, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

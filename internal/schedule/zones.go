package schedule

import (
	_ "embed"
	"strings"
)

//go:embed zones.txt
var zonesTxt string

// Zone is an IANA time zone and its aliases.
type Zone struct {
	Name  string   `json:"name"`
	Links []string `json:"links,omitempty"`
}

// Zones lists the IANA time zones known to the scheduler.
func Zones() []Zone {
	links := map[string][]string{}
	var names []string
	for _, line := range strings.Split(zonesTxt, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[0] == "Z":
			names = append(names, f[1])
		case len(f) == 3 && f[0] == "L":
			links[f[2]] = append(links[f[2]], f[1])
		}
	}
	out := make([]Zone, 0, len(names))
	for _, n := range names {
		out = append(out, Zone{Name: n, Links: links[n]})
	}
	return out
}

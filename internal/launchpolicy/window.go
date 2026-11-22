package launchpolicy

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// span is one window: the days it starts on and its minutes of the day.
// A span whose end is not after its start runs past midnight, into the
// next day; with no times, it lasts the whole day.
type span struct {
	days       [7]bool
	start, end int
	allDay     bool
}

var dayNames = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

// parseWindow reads windows such as "Mon-Thu 20:00-06:00", "Sat,Sun",
// "22:00-06:00" (every day) or several of them separated by ";".
func parseWindow(spec string) ([]span, error) {
	var out []span
	for part := range strings.SplitSeq(spec, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		s := span{allDay: true}
		fields := strings.Fields(part)
		if len(fields) > 2 {
			return nil, fmt.Errorf("Invalid window %q: write days, then a time range, as in \"Mon-Thu 20:00-06:00\".", part)
		}
		days, times := "", ""
		for _, f := range fields {
			if strings.Contains(f, ":") {
				times = f
			} else {
				days = f
			}
		}
		if days == "" {
			for i := range s.days {
				s.days[i] = true
			}
		} else if err := parseDays(days, &s.days); err != nil {
			return nil, fmt.Errorf("Invalid window %q: %v", part, err)
		}
		if times != "" {
			from, to, ok := strings.Cut(times, "-")
			start, err1 := parseClock(from)
			end, err2 := parseClock(to)
			if !ok || err1 != nil || err2 != nil {
				return nil, fmt.Errorf("Invalid window %q: write the times as HH:MM-HH:MM.", part)
			}
			s.start, s.end, s.allDay = start, end, false
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("The window is empty: write days, a time range or both, as in \"Mon-Thu 20:00-06:00\".")
	}
	return out, nil
}

func parseDays(s string, days *[7]bool) error {
	for item := range strings.SplitSeq(s, ",") {
		from, to, isRange := strings.Cut(item, "-")
		a, ok := dayNames[strings.ToLower(from)]
		if !ok {
			return fmt.Errorf("unknown day %q: use Mon, Tue, Wed, Thu, Fri, Sat or Sun.", from)
		}
		b := a
		if isRange {
			if b, ok = dayNames[strings.ToLower(to)]; !ok {
				return fmt.Errorf("unknown day %q: use Mon, Tue, Wed, Thu, Fri, Sat or Sun.", to)
			}
		}
		for d := a; ; d = (d + 1) % 7 {
			days[d] = true
			if d == b {
				break
			}
		}
	}
	return nil
}

func parseClock(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || len(m) != 2 || hh < 0 || mm < 0 || mm > 59 || hh > 24 || (hh == 24 && mm != 0) {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return hh*60 + mm, nil
}

// InWindow reports whether t falls in one of the windows of spec, read in
// the time zone zone.
func InWindow(t time.Time, spec, zone string) (bool, error) {
	spans, err := parseWindow(spec)
	if err != nil {
		return false, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return false, fmt.Errorf("unknown time zone %q", zone)
	}
	t = t.In(loc)
	day, minute := t.Weekday(), t.Hour()*60+t.Minute()
	yesterday := (day + 6) % 7
	for _, s := range spans {
		switch {
		case s.allDay:
			if s.days[day] {
				return true, nil
			}
		case s.start < s.end:
			if s.days[day] && minute >= s.start && minute < s.end {
				return true, nil
			}
		default:
			if (s.days[day] && minute >= s.start) || (s.days[yesterday] && minute < s.end) {
				return true, nil
			}
		}
	}
	return false, nil
}

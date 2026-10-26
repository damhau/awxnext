// Package schedule parses AWX schedule rules (RFC 5545 DTSTART, RRULE,
// EXRULE, RDATE and EXDATE lines) and computes their occurrences.
package schedule

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // time zones without a system database

	"github.com/teambition/rrule-go"
)

// Rule is a parsed schedule rule.
type Rule struct {
	Start   time.Time
	Loc     *time.Location
	TZID    string
	rules   []*rrule.RRule
	exrules []*rrule.RRule
	rdates  []time.Time
	exdates []time.Time
	// Until is the UNTIL of the first RRULE, if any.
	Until time.Time
}

var dtstartRe = regexp.MustCompile(`^DTSTART(?:;TZID=([^:;]+))?:(\d{8}T\d{6})(Z?)$`)

// Parse validates and parses a rule like AWX:
//
//	DTSTART;TZID=America/New_York:20240101T090000 RRULE:FREQ=WEEKLY;BYDAY=MO,WE
func Parse(s string) (*Rule, error) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return nil, errors.New("rrule is required")
	}
	r := &Rule{}
	var rruleTexts, exruleTexts []string
	for _, f := range fields {
		name, _, _ := strings.Cut(f, ":")
		name, _, _ = strings.Cut(name, ";")
		switch strings.ToUpper(name) {
		case "DTSTART":
			if !r.Start.IsZero() {
				return nil, errors.New("Multiple DTSTART is not supported.")
			}
			m := dtstartRe.FindStringSubmatch(f)
			if m == nil {
				return nil, errors.New("Valid DTSTART required in rrule. Value should start with: DTSTART:YYYYMMDDTHHMMSSZ or DTSTART;TZID=<zone>:YYYYMMDDTHHMMSS")
			}
			loc := time.UTC
			if m[1] != "" {
				if m[3] == "Z" {
					return nil, errors.New("DTSTART with TZID must use local time (no Z suffix).")
				}
				l, err := time.LoadLocation(m[1])
				if err != nil {
					return nil, fmt.Errorf("A valid TZID must be provided (e.g., America/New_York): %s", m[1])
				}
				loc, r.TZID = l, m[1]
			} else if m[3] != "Z" {
				return nil, errors.New("DTSTART without TZID must be in UTC (ending with Z).")
			}
			t, err := time.ParseInLocation("20060102T150405", m[2], loc)
			if err != nil {
				return nil, fmt.Errorf("invalid DTSTART: %v", err)
			}
			r.Start, r.Loc = t, loc
		case "RRULE":
			rruleTexts = append(rruleTexts, f[len("RRULE:"):])
		case "EXRULE":
			exruleTexts = append(exruleTexts, f[len("EXRULE:"):])
		case "RDATE", "EXDATE":
			// Parsed once DTSTART (and its zone) is known.
		default:
			return nil, fmt.Errorf("Unsupported rrule component %q.", f)
		}
	}
	if r.Start.IsZero() {
		return nil, errors.New("Valid DTSTART required in rrule. Value should start with: DTSTART:YYYYMMDDTHHMMSSZ")
	}
	if len(rruleTexts) == 0 {
		return nil, errors.New("RRULE required in rrule.")
	}
	if r.TZID == "" {
		r.TZID = "UTC"
	}
	build := func(text string) (*rrule.RRule, error) {
		if err := validateRule(text); err != nil {
			return nil, err
		}
		opt, err := rrule.StrToROptionInLocation(text, r.Loc)
		if err != nil {
			return nil, fmt.Errorf("Invalid rrule: %v", err)
		}
		opt.Dtstart = r.Start
		return rrule.NewRRule(*opt)
	}
	for i, t := range rruleTexts {
		rr, err := build(t)
		if err != nil {
			return nil, err
		}
		if i == 0 && !rr.OrigOptions.Until.IsZero() {
			r.Until = rr.OrigOptions.Until
		}
		r.rules = append(r.rules, rr)
	}
	for _, t := range exruleTexts {
		rr, err := build(t)
		if err != nil {
			return nil, err
		}
		r.exrules = append(r.exrules, rr)
	}
	for _, f := range fields {
		up := strings.ToUpper(f)
		if !strings.HasPrefix(up, "RDATE") && !strings.HasPrefix(up, "EXDATE") {
			continue
		}
		_, value, _ := strings.Cut(f, ":")
		dates, err := rrule.StrToDatesInLoc(value, r.Loc)
		if err != nil {
			return nil, fmt.Errorf("Invalid date list %q: %v", f, err)
		}
		if strings.HasPrefix(up, "RDATE") {
			r.rdates = append(r.rdates, dates...)
		} else {
			r.exdates = append(r.exdates, dates...)
		}
	}
	sort.Slice(r.rdates, func(i, j int) bool { return r.rdates[i].Before(r.rdates[j]) })
	return r, nil
}

// validateRule applies AWX's restrictions on a single RRULE/EXRULE.
func validateRule(text string) error {
	parts := map[string]string{}
	for _, kv := range strings.Split(text, ";") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("Invalid rrule part %q.", kv)
		}
		parts[strings.ToUpper(k)] = strings.ToUpper(v)
	}
	switch parts["FREQ"] {
	case "":
		return errors.New("INTERVAL required in rrule: FREQ is missing.")
	case "SECONDLY":
		return errors.New("SECONDLY is not supported.")
	}
	if c, ok := parts["COUNT"]; ok {
		n, err := strconv.Atoi(c)
		if err != nil || n < 1 || n > 999 {
			return errors.New("COUNT > 999 is unsupported.")
		}
		if _, ok := parts["UNTIL"]; ok {
			return errors.New("RRULE may not contain both COUNT and UNTIL")
		}
	}
	if u, ok := parts["UNTIL"]; ok && !strings.HasSuffix(u, "Z") {
		return errors.New("UNTIL must use UTC time (ending with Z).")
	}
	if strings.Contains(parts["BYDAY"], "-") || regexp.MustCompile(`\d`).MatchString(parts["BYDAY"]) {
		if parts["FREQ"] != "MONTHLY" && parts["FREQ"] != "YEARLY" {
			return errors.New("BYDAY with numeric prefix not supported")
		}
	}
	return nil
}

func (r *Rule) excluded(t time.Time) bool {
	for _, x := range r.exdates {
		if x.Equal(t) {
			return true
		}
	}
	for _, x := range r.exrules {
		if len(x.Between(t, t, true)) > 0 {
			return true
		}
	}
	return false
}

// After returns the first occurrence strictly after t.
func (r *Rule) After(t time.Time) (time.Time, bool) {
	for i := 0; i < 100000; i++ {
		var best time.Time
		for _, rr := range r.rules {
			if n := rr.After(t, false); !n.IsZero() && (best.IsZero() || n.Before(best)) {
				best = n
			}
		}
		for _, d := range r.rdates {
			if d.After(t) {
				if best.IsZero() || d.Before(best) {
					best = d
				}
				break
			}
		}
		if best.IsZero() {
			return time.Time{}, false
		}
		if !r.excluded(best) {
			return best, true
		}
		t = best
	}
	return time.Time{}, false
}

// Next returns up to n occurrences after t.
func (r *Rule) Next(t time.Time, n int) []time.Time {
	var out []time.Time
	for len(out) < n {
		next, ok := r.After(t)
		if !ok {
			break
		}
		out = append(out, next)
		t = next
	}
	return out
}

// Last returns the final occurrence of a bounded rule.
func (r *Rule) Last() (time.Time, bool) {
	for _, rr := range r.rules {
		if rr.OrigOptions.Count == 0 && rr.OrigOptions.Until.IsZero() {
			return time.Time{}, false
		}
	}
	var last time.Time
	t := r.Start.Add(-time.Second)
	for i := 0; i < 100000; i++ {
		n, ok := r.After(t)
		if !ok {
			break
		}
		last, t = n, n
	}
	return last, !last.IsZero()
}

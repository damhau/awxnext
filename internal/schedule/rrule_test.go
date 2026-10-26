package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestParseErrors(t *testing.T) {
	for _, s := range []string{
		"",
		"RRULE:FREQ=DAILY",
		"DTSTART:20240101T090000 RRULE:FREQ=DAILY",
		"DTSTART;TZID=Nowhere/City:20240101T090000 RRULE:FREQ=DAILY",
		"DTSTART:20240101T090000Z",
		"DTSTART:20240101T090000Z RRULE:FREQ=SECONDLY",
		"DTSTART:20240101T090000Z RRULE:FREQ=DAILY;COUNT=1000",
		"DTSTART:20240101T090000Z RRULE:FREQ=DAILY;COUNT=2;UNTIL=20250101T000000Z",
		"DTSTART:20240101T090000Z RRULE:FREQ=DAILY;UNTIL=20250101T000000",
		"DTSTART:20240101T090000Z RRULE:FREQ=WEEKLY;BYDAY=1MO",
		"DTSTART:20240101T090000Z DTSTART:20240101T090000Z RRULE:FREQ=DAILY",
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%q: expected error", s)
		}
	}
}

func TestOccurrences(t *testing.T) {
	r, err := Parse("DTSTART;TZID=America/New_York:20240308T090000 RRULE:FREQ=DAILY;INTERVAL=1;COUNT=5 EXDATE;TZID=America/New_York:20240310T090000")
	if err != nil {
		t.Fatal(err)
	}
	got := r.Next(r.Start.Add(-time.Second), 10)
	var s []string
	for _, x := range got {
		s = append(s, x.UTC().Format(time.RFC3339))
	}
	// DST starts on March 10: 09:00 local moves from 14:00Z to 13:00Z.
	want := "2024-03-08T14:00:00Z 2024-03-09T14:00:00Z 2024-03-11T13:00:00Z 2024-03-12T13:00:00Z"
	if strings.Join(s, " ") != want {
		t.Fatalf("got %v", s)
	}
	last, ok := r.Last()
	if !ok || last.UTC().Format(time.RFC3339) != "2024-03-12T13:00:00Z" {
		t.Fatalf("last %v %v", last, ok)
	}
	if r.TZID != "America/New_York" {
		t.Fatal(r.TZID)
	}
}

func TestMultipleRulesAndExrule(t *testing.T) {
	r, err := Parse("DTSTART:20240101T000000Z RRULE:FREQ=WEEKLY;BYDAY=MO RRULE:FREQ=WEEKLY;BYDAY=FR EXRULE:FREQ=MONTHLY;BYMONTHDAY=5")
	if err != nil {
		t.Fatal(err)
	}
	var days []string
	for _, x := range r.Next(r.Start.Add(-time.Second), 5) {
		days = append(days, x.Format("01-02 Mon"))
	}
	if strings.Join(days, ",") != "01-01 Mon,01-08 Mon,01-12 Fri,01-15 Mon,01-19 Fri" {
		t.Fatalf("got %v", days)
	}
	if _, ok := r.Last(); ok {
		t.Fatal("unbounded rule has no last occurrence")
	}
	if len(Zones()) < 300 {
		t.Fatal("zones missing")
	}
}

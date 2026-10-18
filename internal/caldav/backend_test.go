package caldav

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	webdavcaldav "github.com/emersion/go-webdav/caldav"
)

// TestInstanceBudget pins the estimate used to keep a client from making the
// server expand a recurrence pathologically (issue #39). The expansion itself
// happens inside the library, which has no bound of its own.
func TestInstanceBudget(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		rule    string
		end     time.Time
		overCap bool
	}{
		{"daily over a year", "FREQ=DAILY", start.AddDate(1, 0, 0), false},
		{"daily over a century", "FREQ=DAILY", start.AddDate(100, 0, 0), true},
		{"hourly over a year", "FREQ=HOURLY", start.AddDate(1, 0, 0), false},
		{"hourly over a decade", "FREQ=HOURLY", start.AddDate(10, 0, 0), true},
		{"secondly over an hour", "FREQ=SECONDLY", start.Add(time.Hour), false},
		{"secondly over a day", "FREQ=SECONDLY", start.AddDate(0, 0, 1), true},
		{"monthly over a century", "FREQ=MONTHLY", start.AddDate(100, 0, 0), false},
		{"hourly over fifty years", "FREQ=HOURLY", start.AddDate(50, 0, 0), true},
		{"bounded by COUNT", "FREQ=DAILY;COUNT=5", start.AddDate(100, 0, 0), false},
		{"bounded by UNTIL", "FREQ=DAILY;UNTIL=20260110T000000Z", start.AddDate(100, 0, 0), false},
		{"sparse interval", "FREQ=DAILY;INTERVAL=365", start.AddDate(1, 0, 0), false},
		{"lowercase is still read", "freq=secondly", start.AddDate(0, 0, 1), true},
		{"without FREQ", "COUNT=10", start.AddDate(100, 0, 0), false},
		{"unknown FREQ", "FREQ=EVERYTHING", start.AddDate(100, 0, 0), false},
		{"empty rule", "", start.AddDate(100, 0, 0), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := instanceBudget(c.rule, start, c.end)
			if c.overCap && got <= defaultMaxExpandedInstances {
				t.Errorf("instanceBudget(%q) = %d, want more than %d", c.rule, got, defaultMaxExpandedInstances)
			}
			if !c.overCap && got > defaultMaxExpandedInstances {
				t.Errorf("instanceBudget(%q) = %d, want at most %d", c.rule, got, defaultMaxExpandedInstances)
			}
		})
	}

	// Without a range there is nothing to expand, whatever the rule says.
	if got := instanceBudget("FREQ=SECONDLY", start, start); got != 1 {
		t.Errorf("instanceBudget without a range = %d, want 1", got)
	}
}

// TestOverInstanceBudget walks the components of an object, which is what the
// guard does before delegating a query to the library.
func TestOverInstanceBudget(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	year := timeRange{start: start, end: start.AddDate(1, 0, 0)}
	decade := timeRange{start: start, end: start.AddDate(10, 0, 0)}

	pathological := decodeCalendar(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:pathological\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260102T100000Z\r\n"+
		"RRULE:FREQ=SECONDLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	ordinary := decodeCalendar(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:ordinary\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260102T100000Z\r\n"+
		"RRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	single := decodeCalendar(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n"+
		"BEGIN:VEVENT\r\nUID:single\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260102T100000Z\r\n"+
		"END:VEVENT\r\nEND:VCALENDAR\r\n")

	if !overInstanceBudget(pathological, []timeRange{year}, defaultMaxExpandedInstances) {
		t.Error("a secondly recurrence over a year is not over the budget")
	}
	if overInstanceBudget(pathological, []timeRange{year}, 40000000) {
		t.Error("a secondly recurrence over a year is over a budget it fits in")
	}
	if overInstanceBudget(ordinary, []timeRange{decade}, defaultMaxExpandedInstances) {
		t.Error("a weekly recurrence over a decade is over the budget")
	}
	if overInstanceBudget(single, []timeRange{decade}, defaultMaxExpandedInstances) {
		t.Error("a single event is over the budget")
	}
	// No range in the filter means no expansion to bound.
	if overInstanceBudget(pathological, nil, defaultMaxExpandedInstances) {
		t.Error("without a range, an object cannot be over the budget")
	}
}

// TestQueryRanges covers the nested filters a calendar-query sends: clients put
// the time range in the VEVENT filter, below the VCALENDAR one.
func TestQueryRanges(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)

	filter := webdavcaldav.CompFilter{
		Name: "VCALENDAR",
		Comps: []webdavcaldav.CompFilter{
			{Name: "VEVENT", Start: start, End: end},
			{Name: "VTODO"},
		},
	}
	ranges := queryRanges(&filter)
	if len(ranges) != 1 {
		t.Fatalf("queryRanges returned %d ranges, want 1: %+v", len(ranges), ranges)
	}
	if !ranges[0].start.Equal(start) || !ranges[0].end.Equal(end) {
		t.Errorf("queryRanges returned %v, want [%v %v]", ranges[0], start, end)
	}

	if got := queryRanges(&webdavcaldav.CompFilter{Name: "VCALENDAR"}); len(got) != 0 {
		t.Errorf("queryRanges without a time range returned %+v", got)
	}
}

func decodeCalendar(t *testing.T, ics string) *ical.Component {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(ics)).Decode()
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	return cal.Component
}

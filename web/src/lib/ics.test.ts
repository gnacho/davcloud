import { describe, expect, it } from "vitest";
import { buildEventICS, expandICS } from "./ics";

const meta = { url: "http://dav/e1.ics", etag: '"x"' };

function weeklyMaster(): string {
  return [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//davcloud//test//EN",
    "BEGIN:VEVENT",
    "UID:semanal",
    "DTSTAMP:20250101T000000Z",
    "DTSTART:20250106T090000Z",
    "DTEND:20250106T100000Z",
    "RRULE:FREQ=WEEKLY",
    "SUMMARY:Reunion semanal",
    "END:VEVENT",
    "END:VCALENDAR",
    "",
  ].join("\r\n");
}

describe("expandICS", () => {
  it("expands a weekly recurrence inside the range", () => {
    const evs = expandICS(weeklyMaster(), meta, new Date("2026-10-01T00:00:00Z"), new Date("2026-10-31T23:59:59Z"));
    expect(evs.length).toBeGreaterThanOrEqual(4);
    for (const ev of evs) {
      expect(ev.recurring).toBe(true);
      expect(ev.title).toBe("Reunion semanal");
      expect(ev.start.getUTCDay()).toBe(1); // Monday: DTSTART 2025-01-06 was a Monday
    }
  });

  it("drops a non-recurring event outside the range", () => {
    const ics = buildEventICS({ uid: "p1", title: "Fuera", start: new Date("2026-01-02T10:00:00Z"), end: new Date("2026-01-02T11:00:00Z"), allDay: false });
    const evs = expandICS(ics, meta, new Date("2026-10-01T00:00:00Z"), new Date("2026-10-31T23:59:59Z"));
    expect(evs).toHaveLength(0);
  });

  it("keeps a non-recurring event inside the range", () => {
    const ics = buildEventICS({ uid: "p2", title: "Dentro", start: new Date("2026-10-20T10:00:00Z"), end: new Date("2026-10-20T11:00:00Z"), allDay: false });
    const evs = expandICS(ics, meta, new Date("2026-10-01T00:00:00Z"), new Date("2026-10-31T23:59:59Z"));
    expect(evs).toHaveLength(1);
    expect(evs[0].title).toBe("Dentro");
    expect(evs[0].uid).toBe("p2");
  });

  it("returns nothing for unparseable input", () => {
    expect(expandICS("esto no es ical", meta, new Date(), new Date())).toHaveLength(0);
  });
});

describe("buildEventICS", () => {
  it("produces a parseable single VEVENT with the given UID and title", () => {
    const ics = buildEventICS({ uid: "p3", title: "Con: coma", start: new Date("2026-11-01T10:00:00Z"), end: new Date("2026-11-01T11:00:00Z"), allDay: false });
    const evs = expandICS(ics, meta, new Date("2026-11-01T00:00:00Z"), new Date("2026-11-30T23:59:59Z"));
    expect(evs).toHaveLength(1);
    expect(evs[0].uid).toBe("p3");
    expect(evs[0].title).toBe("Con: coma");
  });
});

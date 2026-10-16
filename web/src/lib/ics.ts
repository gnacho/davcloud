import ICAL from "ical.js";

export interface Ev {
  key: string; // occurrence key: url + uid + recurrence-id
  url: string; // DAV object url
  etag: string;
  uid: string;
  title: string;
  start: Date;
  end: Date;
  allDay: boolean;
  color?: string;
  recurring?: boolean;
  raw: string;
}

// Parse an .ics blob and expand recurrences into concrete occurrences inside
// [rangeStart, rangeEnd] using ical.js' RecurExpansion.
export function expandICS(
  ics: string,
  meta: { url: string; etag: string },
  rangeStart: Date,
  rangeEnd: Date,
  color?: string
): Ev[] {
  try {
    const jcal = ICAL.parse(ics);
    const comp = new ICAL.Component(jcal);
    return comp.getAllSubcomponents("vevent").flatMap((vc: any) => {
      const ev = new ICAL.Event(vc);
      if (!ev.startDate) return [];
      const base = {
        url: meta.url,
        etag: meta.etag,
        uid: ev.uid || crypto.randomUUID(),
        title: ev.summary || "(sin título)",
        allDay: !!ev.startDate.isDate,
        color,
        raw: ics,
      };
      if (ev.isRecurring()) {
        const out: Ev[] = [];
        const it = new ICAL.RecurExpansion({ component: vc, dtstart: ev.startDate });
        let t: any;
        let guard = 0;
        while ((t = it.next()) && guard++ < 400) {
          const s: Date = t.toJSDate();
          if (s.getTime() > rangeEnd.getTime()) break;
          const durSec = ev.duration ? ev.duration.toSeconds() : 3600;
          const e = new Date(s.getTime() + durSec * 1000);
          if (e.getTime() >= rangeStart.getTime()) {
            out.push({ ...base, key: `${meta.url}#${base.uid}#${s.toISOString()}`, start: s, end: e, recurring: true });
          }
        }
        return out;
      }
      const s: Date = ev.startDate.toJSDate();
      const e: Date = ev.endDate ? ev.endDate.toJSDate() : s;
      if (e.getTime() < rangeStart.getTime() || s.getTime() > rangeEnd.getTime()) return [];
      return [{ ...base, key: `${meta.url}#${base.uid}`, start: s, end: e }];
    });
  } catch {
    return [];
  }
}

export interface EventDraft {
  uid?: string;
  title: string;
  start: Date;
  end: Date;
  allDay: boolean;
}

// Build a minimal VEVENT .ics. Editing always normalizes to a single
// non-recurring event (recurrence editing is a TODO).
export function buildEventICS(d: EventDraft): string {
  const cal = new ICAL.Component(["vcalendar", [], []]);
  cal.addPropertyWithValue("prodid", "-//davcloud//ES");
  cal.addPropertyWithValue("version", "2.0");
  const ve = new ICAL.Component("vevent");
  ve.addPropertyWithValue("uid", d.uid ?? crypto.randomUUID());
  ve.addPropertyWithValue("dtstamp", ICAL.Time.fromJSDate(new Date(), false));
  ve.addPropertyWithValue("dtstart", ICAL.Time.fromJSDate(d.start, d.allDay));
  ve.addPropertyWithValue("dtend", ICAL.Time.fromJSDate(d.end, d.allDay));
  ve.addPropertyWithValue("summary", d.title);
  cal.addSubcomponent(ve);
  return cal.toString();
}

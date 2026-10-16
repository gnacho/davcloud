import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import FullCalendar from "@fullcalendar/react";
import dayGridPlugin from "@fullcalendar/daygrid";
import timeGridPlugin from "@fullcalendar/timegrid";
import interactionPlugin from "@fullcalendar/interaction";
import type { DAVCalendar } from "tsdav";
import { loadCalendarObjects, saveEventObject, removeEventObject } from "../lib/dav";
import { buildEventICS, expandICS, type Ev } from "../lib/ics";
import { Btn, Field, Modal, Spinner } from "../components/ui";

// Paleta de colores de Google Calendar
const PALETTE = [
  "#7986cb", "#33b679", "#8e24aa", "#e67c73",
  "#f6c026", "#f5511d", "#039be5", "#616161",
];

export interface CalApi {
  today(): void;
  prev(): void;
  next(): void;
  gotoDate(d: Date): void;
}

interface Draft {
  existing: Ev | null;
  title: string;
  start: Date;
  end: Date;
  allDay: boolean;
}

const CalendarView = forwardRef(function CalendarView(
  {
    calendars,
    visible,
    onChanged,
    onTitleChange,
    createSignal,
  }: {
    calendars: DAVCalendar[];
    visible: Set<string>;
    onChanged: () => void;
    onTitleChange: (t: string) => void;
    createSignal: number;
  },
  ref: React.Ref<CalApi>
) {
  const [events, setEvents] = useState<Ev[]>([]);
  const [loading, setLoading] = useState(true);
  const [draft, setDraft] = useState<Draft | null>(null);
  const range = useRef<{ start: Date; end: Date }>({ start: new Date(), end: new Date() });
  const calRef = useRef<FullCalendar>(null);

  useImperativeHandle(ref, () => ({
    today: () => calRef.current?.getApi().today(),
    prev: () => calRef.current?.getApi().prev(),
    next: () => calRef.current?.getApi().next(),
    gotoDate: (d: Date) => calRef.current?.getApi().gotoDate(d),
  }));

  const colorOf = useCallback(
    (url: string) => {
      const idx = calendars.findIndex((c) => url.startsWith(c.url));
      return PALETTE[((idx % PALETTE.length) + PALETTE.length) % PALETTE.length];
    },
    [calendars]
  );

  const reload = useCallback(async () => {
    const { start, end } = range.current;
    const out: Ev[] = [];
    for (const cal of calendars) {
      if (!visible.has(cal.url)) continue;
      const objs = await loadCalendarObjects(cal);
      for (const o of objs) {
        out.push(...expandICS(o.data, { url: o.url, etag: o.etag }, start, end, colorOf(o.url)));
      }
    }
    setEvents(out);
    setLoading(false);
  }, [calendars, visible, colorOf]);

  useEffect(() => {
    setLoading(true);
    reload().catch(() => setLoading(false));
  }, [reload]);

  useEffect(() => {
    if (createSignal > 0) {
      const now = new Date();
      openCreate(new Date(now.getFullYear(), now.getMonth(), now.getDate(), now.getHours() + 1), false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [createSignal]);

  const fcEvents = useMemo(
    () =>
      events.map((e) => ({
        id: e.key,
        title: e.title,
        start: e.start,
        end: e.end,
        allDay: e.allDay,
        backgroundColor: e.color,
        borderColor: "transparent",
        extendedProps: { ev: e },
      })),
    [events]
  );

  function openCreate(start: Date, allDay: boolean) {
    const end = allDay
      ? new Date(start.getTime() + 24 * 3600 * 1000)
      : new Date(start.getTime() + 3600 * 1000);
    setDraft({ existing: null, title: "", start, end, allDay });
  }

  const save = async () => {
    if (!draft) return;
    const cal = calendars.find((c) =>
      draft.existing ? draft.existing.url.startsWith(c.url) : visible.has(c.url)
    ) ?? calendars.find((c) => visible.has(c.url));
    if (!cal) return;
    const ics = buildEventICS({
      uid: draft.existing?.uid,
      title: draft.title || "(sin título)",
      start: draft.start,
      end: draft.end,
      allDay: draft.allDay,
    });
    await saveEventObject(
      cal,
      draft.existing ? { url: draft.existing.url, etag: draft.existing.etag } : null,
      ics
    );
    setDraft(null);
    await reload();
    onChanged();
  };

  const remove = async () => {
    if (!draft?.existing) return;
    const cal = calendars.find((c) => draft.existing!.url.startsWith(c.url));
    if (!cal) return;
    await removeEventObject(cal, draft.existing.url, draft.existing.etag);
    setDraft(null);
    await reload();
    onChanged();
  };

  const toLocalInput = (d: Date, allDay: boolean) => {
    const pad = (n: number) => String(n).padStart(2, "0");
    const ymd = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    return allDay ? ymd : `${ymd}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  };

  return (
    <div className="h-full">
      {loading && events.length === 0 ? (
        <Spinner text="Cargando calendarios…" />
      ) : (
        <FullCalendar
          ref={calRef}
          plugins={[dayGridPlugin, timeGridPlugin, interactionPlugin]}
          initialView="dayGridMonth"
          headerToolbar={false}
          locale="es"
          height="100%"
          events={fcEvents}
          nowIndicator
          selectable
          dayMaxEventRows={4}
          datesSet={(a) => {
            range.current = { start: a.start, end: a.end };
            onTitleChange(a.view.title);
            reload().catch(() => {});
          }}
          dateClick={(a) => openCreate(a.date, a.allDay)}
          select={(a) => setDraft({ existing: null, title: "", start: a.start, end: a.end ?? a.start, allDay: a.allDay })}
          eventClick={(a) => {
            const ev = (a.event.extendedProps as any).ev as Ev;
            setDraft({ existing: ev, title: ev.title, start: ev.start, end: ev.end, allDay: ev.allDay });
          }}
        />
      )}

      {draft && (
        <Modal title={draft.existing ? "Editar evento" : "Nuevo evento"} onClose={() => setDraft(null)}>
          <div className="space-y-3">
            <Field
              label="Título"
              autoFocus
              value={draft.title}
              onChange={(e) => setDraft({ ...draft, title: e.target.value })}
              onKeyDown={(e) => e.key === "Enter" && save()}
              placeholder="Añadir título"
            />
            <div className="grid grid-cols-2 gap-3">
              <Field
                label={draft.allDay ? "Día" : "Inicio"}
                type={draft.allDay ? "date" : "datetime-local"}
                value={toLocalInput(draft.start, draft.allDay)}
                onChange={(e) => setDraft({ ...draft, start: new Date(e.target.value) })}
              />
              <Field
                label="Fin"
                type={draft.allDay ? "date" : "datetime-local"}
                value={toLocalInput(draft.end, draft.allDay)}
                onChange={(e) => setDraft({ ...draft, end: new Date(e.target.value) })}
              />
            </div>
            {draft.existing?.recurring && (
              <p className="text-xs text-[#e37400] bg-[#fef7e0] border border-[#feefc3] rounded-lg px-3 py-2">
                Evento recurrente: al guardar se convertirá en un evento único.
              </p>
            )}
            <div className="flex items-center justify-between pt-2">
              <div>{draft.existing && <Btn variant="danger" onClick={remove}>Eliminar</Btn>}</div>
              <div className="flex gap-2">
                <Btn onClick={() => setDraft(null)}>Cancelar</Btn>
                <Btn variant="primary" onClick={save}>Guardar</Btn>
              </div>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
});

export default CalendarView;

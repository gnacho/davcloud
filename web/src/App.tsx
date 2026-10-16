import { useCallback, useEffect, useRef, useState } from "react";
import type { DAVAccount, DAVCalendar, DAVAddressBook } from "tsdav";
import { connectCalDAV, connectCardDAV, type Creds } from "./lib/dav";
import CalendarView, { type CalApi } from "./views/CalendarView";
import ContactsView from "./views/ContactsView";
import MiniMonth from "./components/MiniMonth";
import { Btn, Field, Spinner } from "./components/ui";

const PALETTE = ["#7986cb", "#33b679", "#8e24aa", "#e67c73", "#f6c026", "#f5511d", "#039be5", "#616161"];

type Tab = "calendar" | "contacts";

// Logo multicolor estilo Google
function Logo() {
  return (
    <span className="font-display text-xl font-medium tracking-tight select-none">
      <span className="text-[#4285F4]">D</span>
      <span className="text-[#EA4335]">a</span>
      <span className="text-[#FBBC04]">v</span>
      <span className="text-[#4285F4]">C</span>
      <span className="text-[#34A853]">l</span>
      <span className="text-[#EA4335]">o</span>
      <span className="text-[#FBBC04]">u</span>
      <span className="text-[#4285F4]">d</span>
    </span>
  );
}

export default function App() {
  const [creds, setCreds] = useState<Creds | null>(() => {
    const raw = sessionStorage.getItem("davcloud.creds");
    return raw ? JSON.parse(raw) : null;
  });
  const [login, setLogin] = useState({ username: "", password: "" });
  const [error, setError] = useState("");
  const [connecting, setConnecting] = useState(false);

  const [calAccount, setCalAccount] = useState<DAVAccount | null>(null);
  const [cardAccount, setCardAccount] = useState<DAVAccount | null>(null);
  const [calendars, setCalendars] = useState<DAVCalendar[]>([]);
  const [books, setBooks] = useState<DAVAddressBook[]>([]);
  const [visible, setVisible] = useState<Set<string>>(new Set());
  const [tab, setTab] = useState<Tab>("calendar");
  const [title, setTitle] = useState("Calendario");
  const [createSignal, setCreateSignal] = useState(0);

  const calApi = useRef<CalApi | null>(null);

  const boot = useCallback(async (c: Creds) => {
    setConnecting(true);
    setError("");
    try {
      const cal = await connectCalDAV(c);
      const card = await connectCardDAV(c);
      setCalAccount(cal.account);
      setCardAccount(card.account);
      setCalendars(cal.calendars);
      setBooks(card.books);
      setVisible(new Set(cal.calendars.map((x) => x.url)));
      setCreds(c);
      sessionStorage.setItem("davcloud.creds", JSON.stringify(c));
    } catch (e: any) {
      setError(
        e?.status >= 400
          ? "Credenciales rechazadas. Usa tu usuario de OpenCloud y un App Token."
          : `No se pudo conectar: ${e?.message ?? e}`
      );
    } finally {
      setConnecting(false);
    }
  }, []);

  useEffect(() => {
    if (creds) boot(creds).catch(() => {});
  }, [creds, boot]);

  const logout = () => {
    sessionStorage.removeItem("davcloud.creds");
    setCreds(null);
    setCalAccount(null);
    setCardAccount(null);
  };

  if (!creds || !calAccount || !cardAccount) {
    return (
      <div className="min-h-full flex items-center justify-center p-6 bg-white">
        <div className="w-full max-w-sm border border-line rounded-3xl p-8 shadow-2xl shadow-black/10 space-y-6">
          <div className="text-center space-y-1">
            <div className="flex justify-center"><Logo /></div>
            <p className="text-[#5f6368] text-sm">Inicia sesión para ver tus calendarios</p>
          </div>
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              boot(login);
            }}
          >
            <Field label="Usuario OpenCloud" value={login.username}
              onChange={(e) => setLogin({ ...login, username: e.target.value })} autoComplete="username" />
            <Field label="App Token" type="password" value={login.password}
              onChange={(e) => setLogin({ ...login, password: e.target.value })}
              autoComplete="current-password" placeholder="Generado en Ajustes → App tokens" />
            {error && (
              <p className="text-xs text-[#d93025] bg-[#fce8e6] rounded-lg px-3 py-2">{error}</p>
            )}
            <div className="flex justify-end pt-1">
              <Btn variant="primary" type="submit" disabled={connecting || !login.username || !login.password}>
                {connecting ? "Conectando…" : "Siguiente"}
              </Btn>
            </div>
          </form>
        </div>
      </div>
    );
  }

  const navBtn = (active: boolean) =>
    `flex items-center gap-4 w-full px-4 py-2.5 rounded-r-full text-sm font-medium transition ${
      active ? "bg-[#d3e3fd] text-[#001d35]" : "text-[#1f1f1f] hover:bg-[#f0f4f9]"
    }`;

  return (
    <div className="h-full flex flex-col bg-white">
      {/* ── Top bar ── */}
      <header className="flex items-center gap-2 sm:gap-4 px-4 py-2 border-b border-line shrink-0">
        <div className="hidden sm:block"><Logo /></div>
        <button onClick={() => setTab("calendar")} className="sm:hidden mr-1"><Logo /></button>

        <h1 className="text-2xl font-normal text-[#1f1f1f] capitalize ml-2 truncate max-w-[40vw]">
          {tab === "calendar" ? title : "Contactos"}
        </h1>

        {tab === "calendar" && (
          <>
            <div className="flex items-center rounded-full overflow-hidden">
              <button onClick={() => calApi.current?.today()}
                className="px-3 py-1.5 text-sm font-medium text-[#5f6368] hover:bg-[#f1f3f4] rounded-full border border-line">
                Hoy
              </button>
            </div>
            <div className="flex items-center">
              <button onClick={() => calApi.current?.prev()}
                className="p-2 rounded-full hover:bg-[#f1f3f4] text-[#5f6368]" aria-label="Anterior">‹</button>
              <button onClick={() => calApi.current?.next()}
                className="p-2 rounded-full hover:bg-[#f1f3f4] text-[#5f6368]" aria-label="Siguiente">›</button>
            </div>
          </>
        )}

        <div className="flex-1" />

        <div className="hidden md:flex items-center bg-[#f1f3f4] focus-within:bg-white focus-within:shadow focus-within:ring-1 focus-within:ring-accent/40 rounded-lg px-3 w-72 transition">
          <span className="text-[#5f6368] text-sm mr-2">🔍</span>
          <input placeholder="Buscar" className="bg-transparent py-2 text-sm outline-none w-full placeholder:text-[#9aa0a6]" />
        </div>

        <span className="hidden lg:block text-sm text-[#5f6368]">{creds.username}</span>
        <Btn onClick={logout}>Salir</Btn>
      </header>

      <div className="flex-1 flex min-h-0">
        {/* ── Sidebar ── */}
        <aside className="hidden md:flex w-64 flex-col gap-6 p-3 overflow-y-auto shrink-0">
          <div>
            <button
              onClick={() => { setTab("calendar"); setCreateSignal((s) => s + 1); }}
              className="flex items-center gap-3 bg-white border border-line shadow-sm hover:shadow-md hover:bg-[#f8f9fa] rounded-full pl-3 pr-5 py-3 transition ml-1"
            >
              <span className="w-6 h-6 rounded-full bg-accent text-white flex items-center justify-center text-lg leading-none">+</span>
              <span className="text-sm font-medium text-[#5f6368]">Crear</span>
            </button>
          </div>

          <MiniMonth onSelectDay={(d) => { setTab("calendar"); calApi.current?.gotoDate(d); }} />

          <div className="border-t border-line pt-4 space-y-4">
            <div>
              <div className="px-4 pb-2 text-sm font-medium text-[#1f1f1f]">Mis calendarios</div>
              {calendars.map((c, i) => {
                const on = visible.has(c.url);
                const color = PALETTE[i % PALETTE.length];
                return (
                  <div key={c.url} className="flex items-center gap-3 px-4 py-1.5">
                    <button
                      onClick={() => {
                        const next = new Set(visible);
                        if (on) next.delete(c.url); else next.add(c.url);
                        setVisible(next);
                      }}
                      className="w-[18px] h-[18px] rounded-[4px] flex items-center justify-center transition hover:ring-2 hover:ring-black/10"
                      style={{ background: on ? color : "#fff", border: `2px solid ${on ? color : "#dadce0"}` }}
                      aria-label={c.displayName ?? c.url}
                    >
                      {on && <span className="text-white text-[11px] leading-none">✓</span>}
                    </button>
                    <button
                      onClick={() => {
                        const next = new Set(visible);
                        if (on) next.delete(c.url); else next.add(c.url);
                        setVisible(next);
                      }}
                      className="text-sm text-[#1f1f1f] truncate hover:bg-[#f0f4f9] rounded px-1 -mx-1"
                    >
                      {c.displayName ?? c.url}
                    </button>
                  </div>
                );
              })}
            </div>

            <div className="border-t border-line pt-3">
              <button onClick={() => setTab("calendar")} className={navBtn(tab === "calendar")}>
                📅 <span>Calendario</span>
              </button>
              <button onClick={() => setTab("contacts")} className={navBtn(tab === "contacts")}>
                👥 <span>Contactos</span>
              </button>
            </div>
          </div>
        </aside>

        {/* ── Main ── */}
        <main className="flex-1 min-w-0 p-4">
          {tab === "calendar" ? (
            <CalendarView
              ref={(api) => { calApi.current = api; }}
              calendars={calendars}
              visible={visible}
              onChanged={() => {}}
              onTitleChange={setTitle}
              createSignal={createSignal}
            />
          ) : (
            <ContactsView books={books} onChanged={() => {}} />
          )}
        </main>
      </div>
    </div>
  );
}

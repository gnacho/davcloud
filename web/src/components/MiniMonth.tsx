import { useMemo, useState } from "react";

const MESES = [
  "enero", "febrero", "marzo", "abril", "mayo", "junio",
  "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre",
];
const DIAS = ["L", "M", "X", "J", "V", "S", "D"];

// Mini-calendario estilo Google Calendar: mes compacto con hoy resaltado.
export default function MiniMonth({ onSelectDay }: { onSelectDay?: (d: Date) => void }) {
  const today = new Date();
  const [view, setView] = useState({ y: today.getFullYear(), m: today.getMonth() });

  const cells = useMemo(() => {
    const first = new Date(view.y, view.m, 1);
    const offset = (first.getDay() + 6) % 7; // semana empezando en lunes
    const start = new Date(view.y, view.m, 1 - offset);
    return Array.from({ length: 42 }, (_, i) => {
      const d = new Date(start.getFullYear(), start.getMonth(), start.getDate() + i);
      return { d, inMonth: d.getMonth() === view.m };
    });
  }, [view]);

  const move = (delta: number) => {
    const d = new Date(view.y, view.m + delta, 1);
    setView({ y: d.getFullYear(), m: d.getMonth() });
  };

  const isToday = (d: Date) =>
    d.toDateString() === today.toDateString();

  return (
    <div className="text-[#70757a]">
      <div className="flex items-center justify-between mb-1">
        <button
          className="p-1 rounded-full hover:bg-[#f1f3f4] text-sm"
          onClick={() => move(-1)}
          aria-label="Mes anterior"
        >
          ‹
        </button>
        <span className="text-sm font-medium text-[#1f1f1f] capitalize">
          {MESES[view.m]} {view.y}
        </span>
        <button
          className="p-1 rounded-full hover:bg-[#f1f3f4] text-sm"
          onClick={() => move(1)}
          aria-label="Mes siguiente"
        >
          ›
        </button>
      </div>
      <div className="grid grid-cols-7 text-center text-[10px] font-medium">
        {DIAS.map((d) => (
          <div key={d} className="py-0.5">
            {d}
          </div>
        ))}
        {cells.map(({ d, inMonth }) => (
          <button
            key={d.toISOString()}
            disabled={!inMonth}
            onClick={() => onSelectDay?.(d)}
            className={`aspect-square flex items-center justify-center rounded-full text-xs transition
              ${!inMonth ? "invisible" : ""}
              ${isToday(d) ? "bg-accent text-white font-bold" : "hover:bg-[#f1f3f4] text-[#1f1f1f]"}
            `}
          >
            {d.getDate()}
          </button>
        ))}
      </div>
    </div>
  );
}

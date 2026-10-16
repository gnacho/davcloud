import { ReactNode } from "react";

export function Btn({
  children,
  onClick,
  variant = "ghost",
  type = "button",
  disabled,
}: {
  children: ReactNode;
  onClick?: () => void;
  variant?: "primary" | "ghost" | "danger";
  type?: "button" | "submit";
  disabled?: boolean;
}) {
  const styles = {
    primary:
      "bg-accent text-white shadow-sm hover:shadow-md hover:brightness-105 rounded-full",
    ghost:
      "text-[#5f6368] hover:bg-[#f1f3f4] rounded-full",
    danger: "text-[#d93025] hover:bg-[#fce8e6] rounded-full",
  } as const;
  return (
    <button
      type={type}
      disabled={disabled}
      onClick={onClick}
      className={`px-4 py-2 text-sm font-medium transition active:scale-[0.98] disabled:opacity-50 ${styles[variant]}`}
    >
      {children}
    </button>
  );
}

export function Field({
  label,
  ...props
}: { label: string } & React.InputHTMLAttributes<HTMLInputElement>) {
  return (
    <label className="block text-sm">
      <span className="text-[#5f6368] text-xs font-medium">{label}</span>
      <input
        {...props}
        className="mt-1 w-full bg-white border border-line rounded-lg px-3 py-2 text-sm text-[#1f1f1f] placeholder:text-[#9aa0a6] outline-none focus:ring-2 focus:ring-accent/30 focus:border-accent transition"
      />
    </label>
  );
}

export function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/30 p-4 pt-24"
      onClick={onClose}
    >
      <div
        className="w-full max-w-md bg-white border border-line rounded-2xl shadow-2xl p-6 space-y-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <h3 className="font-medium text-lg text-[#1f1f1f]">{title}</h3>
          <button
            onClick={onClose}
            className="text-[#5f6368] hover:text-[#1f1f1f] text-xl leading-none transition"
            aria-label="Cerrar"
          >
            ×
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

export function Avatar({ name }: { name: string }) {
  const initials = name
    .split(" ")
    .map((p) => p[0])
    .filter(Boolean)
    .slice(0, 1)
    .join("")
    .toUpperCase() || "?";
  return (
    <div className="w-9 h-9 rounded-full bg-accent flex items-center justify-center text-white text-sm font-medium shrink-0">
      {initials}
    </div>
  );
}

export function Spinner({ text = "Cargando…" }: { text?: string }) {
  return (
    <div className="flex items-center gap-3 text-[#5f6368] text-sm py-10 justify-center">
      <div className="w-4 h-4 rounded-full border-2 border-accent border-t-transparent animate-spin" />
      {text}
    </div>
  );
}

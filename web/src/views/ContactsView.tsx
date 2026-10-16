import { useCallback, useEffect, useMemo, useState } from "react";
import type { DAVAddressBook } from "tsdav";
import { loadVCards, saveVCard, removeVCard } from "../lib/dav";
import { buildVCard, parseVCard, type Contact, type ContactDraft } from "../lib/vcard";
import { Avatar, Btn, Field, Modal, Spinner } from "../components/ui";

export default function ContactsView({
  books,
  onChanged,
}: {
  books: DAVAddressBook[];
  onChanged: () => void;
}) {
  const [contacts, setContacts] = useState<Contact[]>([]);
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<(ContactDraft & { url?: string; etag?: string }) | null>(null);

  const reload = useCallback(async () => {
    const out: Contact[] = [];
    for (const book of books) {
      const cards = await loadVCards(book);
      for (const c of cards) out.push(parseVCard(c.data, c.url, c.etag));
    }
    out.sort((a, b) => a.fn.localeCompare(b.fn));
    setContacts(out);
    setLoading(false);
  }, [books]);

  useEffect(() => {
    setLoading(true);
    reload().catch(() => setLoading(false));
  }, [reload]);

  const filtered = useMemo(() => {
    const q = query.toLowerCase();
    return contacts.filter(
      (c) =>
        c.fn.toLowerCase().includes(q) ||
        c.email?.toLowerCase().includes(q) ||
        c.tel?.toLowerCase().includes(q)
    );
  }, [contacts, query]);

  const save = async () => {
    if (!draft) return;
    const vcf = buildVCard(draft);
    const book = books[0];
    if (!book) return;
    await saveVCard(book, draft.url && draft.etag ? { url: draft.url, etag: draft.etag } : null, vcf);
    setDraft(null);
    await reload();
    onChanged();
  };

  const remove = async () => {
    if (!draft?.url || !draft?.etag) return;
    await removeVCard(books[0], draft.url, draft.etag);
    setDraft(null);
    await reload();
    onChanged();
  };

  return (
    <div className="flex flex-col h-full gap-4">
      <div className="flex items-center gap-3">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Buscar contactos…"
          className="flex-1 max-w-md bg-[#f1f3f4] focus:bg-white border border-transparent focus:border-line rounded-lg px-4 py-2.5 text-sm outline-none focus:ring-2 focus:ring-accent/30 placeholder:text-[#9aa0a6] transition"
        />
        <Btn variant="primary" onClick={() => setDraft({ fn: "", email: "", tel: "", note: "" })}>
          + Nuevo contacto
        </Btn>
      </div>

      {loading && contacts.length === 0 ? (
        <Spinner text="Cargando contactos…" />
      ) : filtered.length === 0 ? (
        <div className="flex-1 flex items-center justify-center text-[#5f6368] text-sm">
          {contacts.length === 0 ? "Sin contactos todavía." : "Sin resultados."}
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3 overflow-y-auto pb-4">
          {filtered.map((c) => (
            <button
              key={c.url}
              onClick={() => setDraft({ ...c })}
              className="flex items-center gap-3 bg-white hover:bg-[#f8f9fa] border border-line rounded-xl px-4 py-3 text-left transition hover:shadow-md"
            >
              <Avatar name={c.fn} />
              <div className="min-w-0">
                <div className="font-medium text-[#1f1f1f] truncate">{c.fn}</div>
                <div className="text-xs text-[#5f6368] truncate">{c.email ?? c.tel ?? ""}</div>
              </div>
            </button>
          ))}
        </div>
      )}

      {draft && (
        <Modal title={draft.url ? "Editar contacto" : "Nuevo contacto"} onClose={() => setDraft(null)}>
          <div className="space-y-3">
            <Field label="Nombre" autoFocus value={draft.fn}
              onChange={(e) => setDraft({ ...draft, fn: e.target.value })} placeholder="Nombre completo" />
            <Field label="Email" type="email" value={draft.email ?? ""}
              onChange={(e) => setDraft({ ...draft, email: e.target.value })} />
            <Field label="Teléfono" value={draft.tel ?? ""}
              onChange={(e) => setDraft({ ...draft, tel: e.target.value })} />
            <Field label="Nota" value={draft.note ?? ""}
              onChange={(e) => setDraft({ ...draft, note: e.target.value })} />
            <div className="flex items-center justify-between pt-2">
              <div>{draft.url && <Btn variant="danger" onClick={remove}>Eliminar</Btn>}</div>
              <div className="flex gap-2">
                <Btn onClick={() => setDraft(null)}>Cancelar</Btn>
                <Btn variant="primary" onClick={save} disabled={!draft.fn.trim()}>Guardar</Btn>
              </div>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}

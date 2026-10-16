export interface ContactDraft {
  uid?: string;
  fn: string;
  email?: string;
  tel?: string;
  note?: string;
}

export interface Contact extends ContactDraft {
  url: string;
  etag: string;
}

function esc(s: string): string {
  return s
    .replace(/\\/g, "\\\\")
    .replace(/;/g, "\\;")
    .replace(/,/g, "\\,")
    .replace(/\r?\n/g, "\\n");
}

export function buildVCard(d: ContactDraft): string {
  const lines = [
    "BEGIN:VCARD",
    "VERSION:4.0",
    `UID:${d.uid ?? crypto.randomUUID()}`,
    `FN:${esc(d.fn)}`,
    "KIND:individual",
  ];
  if (d.email) lines.push(`EMAIL;TYPE=home:${esc(d.email)}`);
  if (d.tel) lines.push(`TEL;TYPE=cell:${esc(d.tel)}`);
  if (d.note) lines.push(`NOTE:${esc(d.note)}`);
  lines.push("END:VCARD");
  return lines.join("\r\n");
}

export function parseVCard(vcf: string, url: string, etag: string): Contact {
  const get = (name: string): string | undefined => {
    const re = new RegExp("^" + name + "(?:;[^:]*)?:(.+)$", "im");
    const m = vcf.match(re);
    return m ? m[1].trim().replace(/\\(.)/g, "$1") : undefined;
  };
  return {
    url,
    etag,
    uid: get("UID"),
    fn: get("FN") ?? "(sin nombre)",
    email: get("EMAIL"),
    tel: get("TEL"),
    note: get("NOTE"),
  };
}

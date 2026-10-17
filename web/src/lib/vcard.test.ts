import { describe, expect, it } from "vitest";
import { buildVCard, parseVCard } from "./vcard";

describe("buildVCard", () => {
  it("writes a vCard 3.0 with CRLF lines and the given UID", () => {
    const vcf = buildVCard({ uid: "u1", fn: "Ana", email: "a@x.es", tel: "600", note: "nota" });
    const lines = vcf.split("\r\n");
    expect(lines[0]).toBe("BEGIN:VCARD");
    expect(lines[1]).toBe("VERSION:3.0");
    expect(lines).toContain("UID:u1");
    expect(lines).toContain("FN:Ana");
    expect(lines).toContain("EMAIL;TYPE=home:a@x.es");
    expect(lines).toContain("TEL;TYPE=cell:600");
    expect(lines).toContain("NOTE:nota");
    expect(lines[lines.length - 1]).toBe("END:VCARD");
    // vCard 3.0 has no KIND property (that is 4.0).
    expect(vcf).not.toContain("KIND:");
  });

  it("escapes backslash, semicolon, comma and newlines per RFC 6350", () => {
    const vcf = buildVCard({ uid: "u2", fn: "ok", note: "a\\b;c,d\ne" });
    const note = vcf.split("\r\n").find((l) => l.startsWith("NOTE:"));
    expect(note).toBe("NOTE:a\\\\b\\;c\\,d\\ne");
  });

  it("generates a UID when none is given", () => {
    const vcf = buildVCard({ fn: "Ana" });
    expect(vcf.split("\r\n").some((l) => /^UID:[0-9a-f-]{36}$/.test(l))).toBe(true);
  });
});

describe("parseVCard", () => {
  it("reads and unescapes the main fields", () => {
    const vcf = [
      "BEGIN:VCARD",
      "VERSION:3.0",
      "UID:u3",
      "FN:Ana Prueba",
      "EMAIL;TYPE=HOME:ana@example.com",
      "TEL;TYPE=CELL:600000000",
      "NOTE:Linea1\\nLinea2\\, con coma\\; y punto y coma",
      "END:VCARD",
      "",
    ].join("\r\n");
    const c = parseVCard(vcf, "http://x/u3.vcf", '"etag"');
    expect(c.uid).toBe("u3");
    expect(c.fn).toBe("Ana Prueba");
    expect(c.email).toBe("ana@example.com");
    expect(c.tel).toBe("600000000");
    expect(c.note).toBe("Linea1\nLinea2, con coma; y punto y coma");
    expect(c.url).toBe("http://x/u3.vcf");
    expect(c.etag).toBe('"etag"');
  });

  it("survives a card without the optional fields", () => {
    const c = parseVCard("BEGIN:VCARD\r\nUID:u4\r\nFN:X\r\nEND:VCARD\r\n", "u", "e");
    expect(c.email).toBeUndefined();
    expect(c.tel).toBeUndefined();
    expect(c.note).toBeUndefined();
  });
});

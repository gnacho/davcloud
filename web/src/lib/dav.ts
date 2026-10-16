import {
  createAccount,
  fetchCalendars,
  fetchCalendarObjects,
  createCalendarObject,
  updateCalendarObject,
  deleteCalendarObject,
  fetchAddressBooks,
  fetchVCards,
  createVCard,
  updateVCard,
  deleteVCard,
} from "tsdav";
import type { DAVAccount, DAVCalendar, DAVAddressBook } from "tsdav";

export interface Creds {
  username: string;
  password: string; // OpenCloud app token
}

// The SPA is served behind OpenCloud's auth proxy: the browser sends the
// session cookie (or the app token via Basic, handled by tsdav) and OpenCloud
// forwards with X-Remote-User. serverUrl stays same-origin in production.
const origin = () => (import.meta.env.DEV ? "" : location.origin);

export async function connectCalDAV(creds: Creds) {
  const account = await createAccount({
    serverUrl: `${origin()}/caldav`,
    credentials: creds,
    authMethod: "Basic",
    loadCollections: false,
  });
  const calendars = await fetchCalendars({ account });
  return { account, calendars: calendars as DAVCalendar[] };
}

export async function connectCardDAV(creds: Creds) {
  const account = await createAccount({
    serverUrl: `${origin()}/carddav`,
    credentials: creds,
    authMethod: "Basic",
    loadCollections: false,
  });
  const books = await fetchAddressBooks({ account });
  return { account, books: books as DAVAddressBook[] };
}

export async function loadCalendarObjects(calendar: DAVCalendar) {
  return (await fetchCalendarObjects({ calendar })) as {
    url: string;
    etag: string;
    data: string;
    calendar: DAVCalendar;
  }[];
}

export async function saveEventObject(
  calendar: DAVCalendar,
  existing: { url: string; etag: string } | null,
  ics: string
) {
  if (existing) {
    await updateCalendarObject({
      calendarObject: { calendar, url: existing.url, etag: existing.etag, data: ics },
      iCalString: ics,
    });
  } else {
    const uid = extractUID(ics) ?? crypto.randomUUID();
    await createCalendarObject({
      calendar,
      filename: `${uid}.ics`,
      iCalString: ics,
    });
  }
}

export async function removeEventObject(calendar: DAVCalendar, url: string, etag: string) {
  await deleteCalendarObject({ calendarObject: { calendar, url, etag, data: "" } });
}

export async function loadVCards(book: DAVAddressBook) {
  return (await fetchVCards({ addressBook: book })) as {
    url: string;
    etag: string;
    data: string;
  }[];
}

export async function saveVCard(
  book: DAVAddressBook,
  existing: { url: string; etag: string } | null,
  vcf: string
) {
  const uid = extractUID(vcf) ?? crypto.randomUUID();
  const filename = `${uid}.vcf`;
  if (existing) {
    await updateVCard({ vCard: { addressBook: book, url: existing.url, etag: existing.etag, data: vcf }, vCardString: vcf });
  } else {
    await createVCard({ addressBook: book, filename, vCardString: vcf });
  }
  return filename;
}

export async function removeVCard(book: DAVAddressBook, url: string, etag: string) {
  await deleteVCard({ vCard: { addressBook: book, url, etag, data: "" } });
}

function extractUID(icsOrVcf: string): string | null {
  const m = icsOrVcf.match(/^UID:(.+)$/im);
  return m ? m[1].trim() : null;
}

# davcloud

[English](README.md) | [Español](README.es.md)

A **CalDAV + CardDAV server in Go** with two jobs:

1. **Base for OpenCloud**: drop-in replacement for the Radicale sidecar of an
   OpenCloud deployment (same port, same `X-Remote-User` contract, same data
   volume, zero migration), with a modern web UI (React + Tailwind +
   FullCalendar) served by the same binary.
2. **Generic Radicale replacement**: any deployment that serves contacts and
   calendars over Radicale's `multifilesystem` layout behind a proxy that
   injects the identity in a header can swap Radicale for davcloud by mounting
   the same data volume.

The parity goal is simple and measurable: DAVx⁵, Thunderbird, iOS and the
OpenCloud web extensions (agenda, contacts) must not notice the difference.

It is a **single static binary** (~7 MB): the DAV server and the web UI in one
executable, no runtime dependencies.

## Contract with the proxy (identical to Radicale's)

| Aspect | Value |
|---|---|
| Port | `5232` (internal network only, never public) |
| Identity | `X-Remote-User` header injected by the proxy |
| Routes | `/caldav`, `/carddav` (with `-dav-root`: served at `/`, prefix stripped) |
| Data | Radicale `multifilesystem` layout → same volume, zero migration |
| Defaults | creates "Personal Calendar" and "Personal Address Book" per user |
| Client auth | user + app token (OpenCloud: Settings → App tokens) |

⚠️ **Security**: the server blindly trusts `X-Remote-User`. An attacker with
direct access to port 5232 can impersonate any user. Never publish the port;
it must only be reachable through the proxy.

## Architecture

```
cmd/server        → HTTP: /caldav, /carddav, /.well-known/*, / (SPA)
internal/dav      → PROPFIND/PROPPATCH/PUT/GET/DELETE/MKCOL/OPTIONS/sync-collection
                    with Radicale's flat URL layout
internal/auth     → X-Remote-User middleware + dev mode (-dev-user)
internal/store    → filesystem, Radicale-compatible layout (.Radicale.props)
internal/caldav   → CalDAV backend delegated to go-webdav (REPORT queries)
internal/carddav  → CardDAV backend delegated to go-webdav (REPORT queries)
web/              → React 18 + Vite + Tailwind v4 + FullCalendar + tsdav + ical.js
deploy/           → migration runbook (binary + systemd)
```

### Why there is an own DAV handler

go-webdav v0.7 derives the resource type by counting path segments and
expects one more level than Radicale (`/user/calendars/cal1/x.ics` vs the flat
`/user/cal1/x.ics`). With the flat layout its PROPFIND lists nothing, its
MKCOL and its CardDAV DELETE answer 403, and `Prefix` cannot fix it because it
always trims the same path. `internal/dav` implements PROPFIND (Depth
0/1/infinity, `prop`/`allprop`/`propname`, 404 propstat for unknown
properties), MKCOL/MKCALENDAR, PROPPATCH (with RFC 4918 atomicity), DELETE,
OPTIONS, `sync-collection` (RFC 6578) and `/.well-known/caldav|carddav` with
Radicale's semantics, and delegates the report queries (calendar-query,
calendar-multiget and their CardDAV counterparts) to go-webdav, whose filter
engine already expands RRULE/RDATE/EXDATE when evaluating time-ranges.

Items are stored **byte for byte** as the client sent them (like Radicale
does), so the data volume stays interchangeable with a Radicale one in both
directions. Collection metadata is read and written in Radicale's own
`.Radicale.props` format (including the comma separated
supported-calendar-component-set string).

## Development

Terminal 1 (backend, dev mode with a forced user):

```bash
go build ./... && go test ./...
go run ./cmd/server -addr 127.0.0.1:5232 -data-dir ./data -dev-user alice
```

Terminal 2 (frontend dev server proxying the DAV endpoints with
`X-Remote-User`):

```bash
cd web
npm ci
DAVCLOUD_DEV_USER=alice npm run dev   # http://localhost:5173
```

## Production with OpenCloud

See [deploy/README.md](deploy/README.md) for the migration runbook and the
rollback drill. In short: davcloud
listens on the same `127.0.0.1:5232` Radicale used, over the same data
volume, so the proxy needs no changes and the swap (and its rollback) is a
service switch. Clients (DAVx⁵, iOS, GNOME, Thunderbird...) keep the exact
same configuration: base URL `https://<your-domain>/caldav` and `.../carddav`,
user + app token.

## Status (verified, not promised)

Done and covered by tests (37) plus live curl runs:

- PROPFIND with the flat Radicale layout: Depth 0/1/infinity, `prop`,
  `allprop`, `propname`, 404 propstat for unknown properties.
- PROPPATCH with RFC 4918 atomicity: rename, `calendar-color`, descriptions;
  client properties persisted with Radicale's canonical keys
  (`ICAL:calendar-color`, ...).
- REPORT `calendar-query`, `calendar-multiget` (delegated), `sync-collection`
  (RFC 6578, own implementation), and recurrence expansion verified in
  time-range queries, tolerant of broken RRULEs.
- Atomic writes (temp + fsync + rename) and conformant `If-Match` /
  `If-None-Match` preconditions; canonical quoted ETag; GET with 304s.
- Byte-for-byte item storage (CRLF, UTF-8, `X-` properties, parameters,
  `VALARM`); vCard 3.0 is not rewritten to 4.0; UID must match the resource
  name (like Radicale).
- Radicale volume compatibility verified end to end: a copy of a real
  production volume mounted into davcloud lists, serves and writes items with
  identical bytes.

Backlog (tracked in issues):

- CI workflow for the Go suite and the web build.
- Production migration (phase 4) and soak period + rollback drill (phase 5).
- iMIP invitations, calendar sharing (phase 6; needs ACLs the proxy does not
  provide).
- Validation with real clients (DAVx⁵, iOS, GNOME Online Accounts).

## License

AGPL-3.0 (see LICENSE). go-webdav is MIT; FullCalendar MIT; ical.js MPL-2.0.

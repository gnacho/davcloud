# davcloud

[![ci](https://github.com/gnacho/davcloud/actions/workflows/ci.yml/badge.svg)](https://github.com/gnacho/davcloud/actions/workflows/ci.yml)

[English](README.md) | [Español](README.es.md)

A single-binary **CalDAV + CardDAV server in Go**, built as a drop-in
replacement for [Radicale](https://radicale.org/): it speaks the same URLs,
reads and writes the same on-disk data layout, and gets its users from the
same header a reverse proxy injects. Designed to sit behind an
[OpenCloud](https://github.com/opencloud-eu/opencloud) deployment (its own
full-stack reference ships Radicale exactly like this), but it works behind
any proxy that authenticates and injects the identity.

One static binary (~7 MB), no runtime dependencies, no database: the binary
and the data volume are the whole deployment (a container image is published
as well, for those who prefer one).

## What it does

- **CalDAV**: calendar discovery, `calendar-query` and `calendar-multiget`
  REPORTs (with server-side RRULE/RDATE/EXDATE expansion in time-range
  queries), `sync-collection` (RFC 6578) for incremental sync, MKCALENDAR,
  free-busy query.
- **CardDAV**: address book discovery, `addressbook-query` and
  `addressbook-multiget` REPORTs, MKCOL, the same sync endpoint.
- **Shared DAV plumbing**: PROPFIND (Depth 0/1/infinity, `prop`/`allprop`/
  `propname`, 404 propstat for unknown properties), PROPPATCH with RFC 4918
  atomicity (rename, `calendar-color`, descriptions), conditional PUT/GET
  (`If-Match`, `If-None-Match`, 304s), DELETE, MKCOL/MKCALENDAR, OPTIONS and
  `/.well-known/caldav|carddav` discovery.
- **Multi-user, proxy-agnostic auth**: every request carries
  `X-Remote-User`; the server never asks for passwords. Pair it with any
  authenticating reverse proxy (OpenCloud's proxy, Authelia, basic auth...).
- **Automatic per-user bootstrap**: first access creates the user's
  "Personal Calendar" and "Personal Address Book", like Radicale's
  predefined collections.
- Plus an optional built-in web UI (React + FullCalendar) for standalone
  use, served by the same binary.

What it deliberately does **not** do (yet): invitations (iMIP), calendar
sharing between users, and server-side sorting of recurrences in the web UI.

## How it works

```
clients (DAVx5, Thunderbird, iOS, web apps)
      |  /caldav/*  /carddav/*
      v
authenticating reverse proxy  (auth -> X-Remote-User: <user id>)
      |  prefix stripped, X-Script-Name set
      v
davcloud  127.0.0.1:5232  (one static binary)
      |
      v
data volume, Radicale "multifilesystem" layout

<volume>/collection-root/<user>/<collection>/.Radicale.props
<volume>/collection-root/<user>/<collection>/<item>.ics|.vcf
```

- **Flat URLs, Radicale-compatible**: `/caldav/<user>/<collection>/<item>`.
  Most of the DAV plumbing lives in `internal/dav`, written for exactly this
  layout. `go-webdav` (the library) is still used where it shines: the
  `calendar-query`/`multiget` filter engine, which even expands recurrences.
- **Byte-for-byte storage**: items are saved exactly as the client sent them
  and served back unchanged, so a volume written by davcloud is
  indistinguishable from one written by Radicale - in both directions.
  Collection metadata lives in Radicale's own `.Radicale.props` files (same
  keys, including the comma separated supported-calendar-component-set).
- **Server-managed names are reserved**: a name with a leading dot (a
  collection's own `.Radicale.props`, the `.tmp-*` files of atomic writes) or a
  trailing tilde does not exist as far as clients are concerned. `PUT` on such a
  name answers 400 and `DELETE`/`GET` answer 404, so no client can break a
  collection by writing over, or deleting, its metadata. Radicale enforces the
  same rule through `pathutils.is_safe_filesystem_path_component`.
- **Identity is a header**: `X-Remote-User` is trusted blindly, so the port
  must never be reachable without the authenticating proxy in front. In
  `-dav-root` mode the proxy strips the `/caldav`/`/carddav` prefixes and
  marks the endpoint with `X-Script-Name`; davcloud still emits prefixed
  hrefs, exactly like Radicale does.
- **One codebase, two roles**: the same binary serves the DAV API and (in
  standalone mode) a small React web UI. Behind OpenCloud the web apps are
  the UI, so davcloud runs with `-dav-root` and does not serve it.

## How to install it

To build it you need Go 1.24+ (plus `npm` for the web UI). No database, no
runtime dependencies. A released binary or the container image is
self-contained.

**Download a release** (tags move in steps of `0.2`: `v0.2.0`, `v0.4.0`, ...):

```bash
curl -LO https://github.com/gnacho/davcloud/releases/latest/download/davcloud-linux-amd64
curl -LO https://github.com/gnacho/davcloud/releases/latest/download/davcloud-linux-amd64.sha256
sha256sum -c davcloud-linux-amd64.sha256
chmod +x davcloud-linux-amd64
./davcloud-linux-amd64 -version
```

Use `arm64` instead of `amd64` on ARM machines.

**Container**:

```bash
mkdir -p davcloud-data
docker run -d --name davcloud \
  --user "$(id -u):$(id -g)" \
  -p 127.0.0.1:5232:5232 \
  -v "$PWD/davcloud-data:/data" \
  ghcr.io/gnacho/davcloud:latest \
  -addr :5232 -data-dir /data -ui-dir /app/ui -dev-user alice
curl -s -o /dev/null -w '%{http_code}\n' -X PROPFIND -H 'Depth: 0' \
  http://127.0.0.1:5232/caldav/
# 207
```

The image runs as uid 1000, hence `--user` with your own ids so that it can
write into the bind-mounted directory. `-dev-user` is the development shortcut
that trusts every request as that user; in production the identity comes from
the `X-Remote-User` header injected by the proxy, so the port must stay
unreachable for anybody else.

**Build**

```bash
git clone https://github.com/gnacho/davcloud && cd davcloud
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --tags --always) -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o davcloud ./cmd/server
./davcloud -version
```

**Try it standalone** (identity simulated, no proxy needed):

```bash
./davcloud -addr 127.0.0.1:5232 -data-dir ./data -dev-user alice
# in another terminal: any CalDAV client against http://127.0.0.1:5232/caldav/
```

**Production behind OpenCloud** (the tested path; full runbook with backup
and rollback in [deploy/README.md](deploy/README.md)):

1. Back up Radicale's data directory, then swap services on the same port:

   ```ini
   # /etc/systemd/system/davcloud.service (excerpt)
   [Service]
   ExecStart=/usr/local/bin/davcloud -addr 127.0.0.1:5232 -data-dir /var/lib/radicale/collections -dav-root
   User=radicale
   ReadWritePaths=/var/lib/radicale/
   ```

2. Make sure the proxy routes point at it and inject the identity (this is
   what an OpenCloud `proxy.yaml` looks like with Radicale, and davcloud
   uses the exact same shape):

   ```yaml
   additional_policies:
     - name: default
       routes:
         - endpoint: /caldav/
           backend: http://127.0.0.1:5232
           remote_user_header: X-Remote-User
           skip_x_access_token: true
           additional_headers:
             - X-Script-Name: /caldav
         - endpoint: /carddav/
           backend: http://127.0.0.1:5232
           remote_user_header: X-Remote-User
           skip_x_access_token: true
           additional_headers:
             - X-Script-Name: /carddav
   ```

3. Verify (as the proxy would call it, with your real user id):

   ```bash
   curl -s -X PROPFIND -H 'Depth: 0' \
     -H 'X-Script-Name: /caldav' -H 'X-Remote-User: <user-id>' \
     http://127.0.0.1:5232/ | grep -o '<D:href>[^<]*</D:href>'
   # /caldav/<user-id>/
   ```

Rollback is a service switch: stop davcloud, start Radicale over the same
volume (the layout and metadata are byte-compatible both ways).

**Limits** (defaults chosen to match Radicale's own configuration):

| Flag | Default | What it bounds |
|---|---|---|
| `-max-request-size` | 100 MiB | any request body; over it the answer is 413 |
| `-max-item-size` | 10 MiB | a single calendar object or vCard, announced to clients as `max-resource-size` |
| `-max-expand` | 10000 | recurrence instances one object may expand to while a time-range query is matched; over it the object is matched on its plain dates |

Each one can also be set through the environment (`DAVCLOUD_MAX_REQUEST_SIZE`,
`DAVCLOUD_MAX_ITEM_SIZE`, `DAVCLOUD_MAX_EXPAND`). The HTTP server is built with
explicit timeouts (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`,
`IdleTimeout`, `MaxHeaderBytes`) rather than Go's defaults, which are none.

## Status

In daily use as the DAV server behind the author's OpenCloud deployment. The
rollback drill passed on 2026-10-18: Radicale 3.5.3 served the same volume
again, read every item davcloud had written (same UIDs and fields, same
collection properties) and left the files untouched, so going back is a service
switch. The soak period is still running, so treat it as software in active
development. The phase list and what is left are in
[PLAN.md](PLAN.md). Covered by 40 Go tests and 10 web tests, CI on every push
(test/lint/security/web), and a migration dry run against a copy of a real
production volume. Known limitations are tracked as issues: iMIP, calendar
sharing, recurrence editing in the web UI.

## License

AGPL-3.0 (see LICENSE). Dependencies: go-webdav MIT, go-ical MIT, go-vcard
MIT, FullCalendar MIT, ical.js MPL-2.0.

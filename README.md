# davcloud

Servidor **CalDAV + CardDAV en Go** con dos cometidos:

1. **Base para OpenCloud**: reemplazo drop-in del sidecar Radicale de un
   despliegue de OpenCloud, con una web UI moderna (React + Tailwind +
   FullCalendar) servida por el mismo binario.
2. **Reemplazo genérico de Radicale**: cualquier despliegue que sirva
   contactos y calendarios sobre el layout `multifilesystem` de Radicale
   detrás de un proxy que inyecte la identidad en una cabecera puede
   sustituir Radicale por davcloud montando el mismo volumen de datos.

El objetivo de paridad es simple y medible: que DAVx⁵, Thunderbird, iOS y
las extensiones web de OpenCloud (agenda, contacts) no noten la diferencia.

## Contrato con el proxy (idéntico al de Radicale)

| Aspecto | Valor |
|---|---|
| Puerto | `5232` (solo red interna de Docker, nunca público) |
| Identidad | cabecera `X-Remote-User` inyectada por el proxy |
| Rutas | `/caldav`, `/carddav` (las que el proxy ya reenvía) |
| Datos | layout Radicale `multifilesystem` → mismo volumen, migración cero |
| Defaults | crea "Personal Calendar" y "Personal Address Book" por usuario |
| Auth de clientes | usuario + App Token (OpenCloud: Ajustes → App tokens) |

⚠️ **Seguridad**: el servidor confía ciegamente en `X-Remote-User`. Un
atacante con acceso directo al puerto 5232 puede suplantar a cualquier
usuario. El contenedor no debe publicar puertos; solo es accesible vía el
proxy.

## Arquitectura

```
cmd/server        → HTTP: /caldav, /carddav, /.well-known/*, / (SPA)
internal/dav      → PROPFIND/MKCOL/DELETE/OPTIONS con el layout plano de Radicale
internal/auth     → middleware X-Remote-User + modo dev (-dev-user)
internal/store    → filesystem, layout compatible con Radicale (.Radicale.props)
internal/caldav   → backend CalDAV delegado en go-webdav (REPORT/GET/PUT/HEAD)
internal/carddav  → backend CardDAV delegado en go-webdav (REPORT/GET/PUT/HEAD)
web/              → React 18 + Vite + Tailwind v4 + FullCalendar + tsdav + ical.js
deploy/           → overlay docker-compose para opencloud-compose
```

### Por qué hay un handler DAV propio

go-webdav v0.7 deduce el tipo de recurso contando segmentos de la ruta y
espera un nivel más que Radicale (`/user/calendars/cal1/x.ics` frente al
plano `/user/cal1/x.ics`). Con el layout plano, su PROPFIND lista vacío, su
MKCOL y el DELETE de CardDAV responden 403, y el `Prefix` no lo puede
arreglar porque siempre recorta el mismo path. `internal/dav` implementa
PROPFIND (Depth 0/1/infinity, `prop`/`allprop`/`propname`, propstat 404
para propiedades desconocidas), MKCOL/MKCALENDAR, DELETE, OPTIONS y
`/.well-known/caldav|carddav` con la semántica de Radicale, y delega el
resto (REPORT, GET, HEAD, PUT) en go-webdav.

## Desarrollo local

Terminal 1 (backend, modo dev con usuario forzado):

```bash
go build ./... && go test ./...
go run ./cmd/server -addr 127.0.0.1:5232 -data-dir ./data -dev-user alice
```

Terminal 2 (frontend con proxy inyectando X-Remote-User):

```bash
cd web
npm install
DAVCLOUD_DEV_USER=alice npm run dev   # http://localhost:5173
```

## Producción con OpenCloud

1. `docker build -t davcloud:latest .`
2. Copia `deploy/opencloud-davcloud.yml` a tu checkout de opencloud-compose,
   ajusta `context:` y cámbialo en `COMPOSE_FILE` (ver comentarios del YAML).
3. `docker compose up -d` - Radicale desaparece, davcloud ocupa su lugar con
   los mismos datos.

Los clientes (DAVx⁵, iOS, GNOME, Thunderbird...) se configuran exactamente
igual que con Radicale: URL base `https://<tu-dominio>/caldav` y
`.../carddav`, usuario + app token.

## Estado (verificado, no promesas)

Hecho y con tests (21) + prueba de humo con `curl`:

- Compila con `go mod tidy` (`go build`, `go vet`, `gofmt` limpios).
- PROPFIND con el layout plano de Radicale: Depth 0/1/infinity, `prop`,
  `allprop`, `propname`, propstat 404 para propiedades desconocidas.
- Escrituras atómicas (temp + fsync + rename) y condicionales
  `If-Match` / `If-None-Match` con semántica conforme (412).
- ETag canónico (quoted-string de 64 hex) idéntico en header, `getetag` y
  precondiciones.
- REPORT `calendar-query`, `calendar-multiget` (delegados en go-webdav).
- MKCALENDAR/MKCOL, DELETE, OPTIONS, `/.well-known/caldav|carddav`.
- Layout en disco byte-compatible con Radicale (`collection-root/<user>/...`,
  `.Radicale.props` con las mismas claves), lectura tolerante de claves
  extra.
- Fidelidad de datos: CRLF, UTF-8, `X-` props, `ATTENDEE` con parámetros,
  `VALARM`; el vCard 3.0 no se reescribe a 4.0.

Pendiente (issues abiertos):

- PROPPATCH (hoy 501): renombrar colección, `calendar-color`, descripciones.
- REPORT `sync-collection` (hoy 400; los clientes caen a PROPFIND + getetag).
- Expansión de RRULE y time-range queries en el servidor (hoy el filtro es
  conservador y los clientes expanden en local).
- PUT tolerante con el `Content-Type` (hoy exige `text/calendar`/`text/vcard`).
- Fidelidad byte a byte: go-ical/go-vcard reordenan propiedades al
  re-encodear (solo orden, no contenido).
- Compartición entre usuarios (requiere ACL propias; el proxy solo inyecta
  identidad, no grupos).
- Invitaciones iMIP.
- Validación con clientes reales (DAVx⁵, iOS, GNOME Online Accounts) y soak
  con ambos servidores antes de sustituir Radicale.

## Licencia

Pendiente de decidir (ver issue abierto). go-webdav es MIT; FullCalendar
MIT; ical.js MPL-2.0.

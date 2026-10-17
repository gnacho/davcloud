# davcloud

[English](README.md) | [Español](README.es.md)

Un **servidor CalDAV + CardDAV en Go** con dos cometidos:

1. **Base para OpenCloud**: reemplazo drop-in del sidecar Radicale de un
   despliegue de OpenCloud (mismo puerto, mismo contrato `X-Remote-User`,
   mismo volumen de datos, migración cero), con una web UI moderna
   (React + Tailwind + FullCalendar) servida por el mismo binario.
2. **Reemplazo genérico de Radicale**: cualquier despliegue que sirva
   contactos y calendarios sobre el layout `multifilesystem` de Radicale
   detrás de un proxy que inyecte la identidad en una cabecera puede
   sustituir Radicale por davcloud montando el mismo volumen.

El objetivo de paridad es simple y medible: que DAVx⁵, Thunderbird, iOS y las
extensiones web de OpenCloud (agenda, contacts) no noten la diferencia.

Es un **único binario estático** (~7 MB): servidor DAV y web UI en un
ejecutable, sin dependencias de runtime.

## Contrato con el proxy (idéntico al de Radicale)

| Aspecto | Valor |
|---|---|
| Puerto | `5232` (solo red interna, nunca público) |
| Identidad | cabecera `X-Remote-User` inyectada por el proxy |
| Rutas | `/caldav`, `/carddav` (con `-dav-root`: servidas en `/`, prefijo eliminado) |
| Datos | layout Radicale `multifilesystem` → mismo volumen, migración cero |
| Defaults | crea "Personal Calendar" y "Personal Address Book" por usuario |
| Auth de clientes | usuario + app token (OpenCloud: Ajustes → App tokens) |

⚠️ **Seguridad**: el servidor confía ciegamente en `X-Remote-User`. Un
atacante con acceso directo al puerto 5232 puede suplantar a cualquier
usuario. Nunca publicar el puerto: solo debe ser accesible vía el proxy.

## Arquitectura

```
cmd/server        → HTTP: /caldav, /carddav, /.well-known/*, / (SPA)
internal/dav      → PROPFIND/PROPPATCH/PUT/GET/DELETE/MKCOL/OPTIONS/sync-collection
                    con el layout plano de Radicale
internal/auth     → middleware X-Remote-User + modo dev (-dev-user)
internal/store    → filesystem, layout compatible con Radicale (.Radicale.props)
internal/caldav   → backend CalDAV delegado en go-webdav (REPORT queries)
internal/carddav  → backend CardDAV delegado en go-webdav (REPORT queries)
web/              → React 18 + Vite + Tailwind v4 + FullCalendar + tsdav + ical.js
deploy/           → runbook de migración + overlay compose
```

### Por qué hay un handler DAV propio

go-webdav v0.7 deduce el tipo de recurso contando segmentos de la ruta y
espera un nivel más que Radicale (`/user/calendars/cal1/x.ics` frente al
plano `/user/cal1/x.ics`). Con el layout plano, su PROPFIND lista vacío, su
MKCOL y su DELETE de CardDAV responden 403, y `Prefix` no lo puede arreglar
porque siempre recorta el mismo path. `internal/dav` implementa PROPFIND
(Depth 0/1/infinity, `prop`/`allprop`/`propname`, propstat 404 para
propiedades desconocidas), MKCOL/MKCALENDAR, PROPPATCH (con atomicidad RFC
4918), DELETE, OPTIONS, `sync-collection` (RFC 6578) y
`/.well-known/caldav|carddav` con la semántica de Radicale, y delega las
consultas (calendar-query, calendar-multiget y sus equivalentes de CardDAV)
en go-webdav, cuyo motor de filtros ya expande RRULE/RDATE/EXDATE al evaluar
los rangos de tiempo.

Los items se guardan **byte a byte** tal como los envió el cliente (igual que
Radicale), así que el volumen de datos sigue siendo intercambiable con uno de
Radicale en ambos sentidos. Los metadatos de colección se leen y escriben en
el formato propio de Radicale (`.Radicale.props`, incluido el string separado
por comas del supported-calendar-component-set).

## Desarrollo

Terminal 1 (backend, modo dev con usuario forzado):

```bash
go build ./... && go test ./...
go run ./cmd/server -addr 127.0.0.1:5232 -data-dir ./data -dev-user alice
```

Terminal 2 (frontend con proxy inyectando `X-Remote-User`):

```bash
cd web
npm ci
DAVCLOUD_DEV_USER=alice npm run dev   # http://localhost:5173
```

## Producción con OpenCloud

El runbook de migración (camino systemd nativo y camino compose) y el drill
de rollback están en [deploy/README.md](deploy/README.md). En resumen:
davcloud escucha en el mismo `127.0.0.1:5232` que usaba Radicale, sobre el
mismo volumen de datos, así que el proxy no necesita cambios y el swap (y su
rollback) es un cambio de servicio. Los clientes (DAVx⁵, iOS, GNOME,
Thunderbird...) mantienen su configuración exacta: URL base
`https://<tu-dominio>/caldav` y `.../carddav`, usuario + app token.

## Estado (verificado, no promesas)

Hecho y cubierto por tests (37) y pruebas con `curl` en vivo:

- PROPFIND con el layout plano de Radicale: Depth 0/1/infinity, `prop`,
  `allprop`, `propname`, propstat 404 para propiedades desconocidas.
- PROPPATCH con atomicidad RFC 4918: renombrar, `calendar-color`,
  descripciones; props de cliente persistidas con las claves canónicas de
  Radicale (`ICAL:calendar-color`, ...).
- REPORT `calendar-query`, `calendar-multiget` (delegados), `sync-collection`
  (RFC 6578, implementación propia), y expansión de recurrencias verificada
  en las queries de rango, tolerante con RRULE rotas.
- Escrituras atómicas (temp + fsync + rename) y condicionales `If-Match` /
  `If-None-Match` conformes; ETag canónico entrecomillado; GET con 304.
- Items guardados byte a byte (CRLF, UTF-8, props `X-`, parámetros,
  `VALARM`); el vCard 3.0 no se reescribe a 4.0; el UID debe coincidir con el
  nombre del recurso (igual que Radicale).
- Compatibilidad con el volumen de Radicale verificada de extremo a extremo:
  una copia de un volumen real de producción montada en davcloud lista,
  sirve y escribe items con bytes idénticos.

Backlog (seguido en issues):

- Workflow de CI para la suite Go y el build web.
- Construcción y publicación de la imagen de contenedor (necesita una
  máquina con motor de contenedores).
- Migración de producción (fase 4) y soak + drill de rollback (fase 5).
- Invitaciones iMIP, compartición de calendarios (fase 6; requiere ACLs que
  el proxy no proporciona).
- Validación con clientes reales (DAVx⁵, iOS, GNOME Online Accounts).

## Licencia

AGPL-3.0 (ver LICENSE). go-webdav es MIT; FullCalendar MIT; ical.js MPL-2.0.

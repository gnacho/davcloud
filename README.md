# DavCloud

Servidor **CalDAV + CardDAV en Go** pensado como reemplazo drop-in del
sidecar Radicale de un despliegue de **OpenCloud**, con una web UI moderna
(React + Tailwind + FullCalendar) servida por el mismo binario.

## Contrato con OpenCloud (idéntico al de Radicale)

| Aspecto | Valor |
|---|---|
| Puerto | `5232` (solo red interna de Docker, nunca público) |
| Identidad | cabecera `X-Remote-User` inyectada por el proxy de OpenCloud |
| Rutas | `/caldav`, `/carddav` (las que el proxy ya reenvía) |
| Datos | layout Radicale `multifilesystem` → mismo volumen, migración cero |
| Defaults | crea "Personal Calendar" y "Personal Address Book" por usuario |
| Auth de clientes | usuario OpenCloud + **App Token** (Ajustes → App tokens) |

⚠️ **Seguridad**: el servidor confía ciegamente en `X-Remote-User`. Un
atacante con acceso directo al puerto 5232 puede suplantar a cualquier
usuario. El contenedor no debe publicar puertos; solo es accesible vía
OpenCloud.

## Arquitectura

```
cmd/server        → HTTP: /caldav (go-webdav caldav), /carddav (carddav), / (SPA)
internal/auth     → middleware X-Remote-User + modo dev (-dev-user)
internal/store    → filesystem, layout compatible con Radicale (.Radicale.props)
internal/caldav   → backend CalDAV (go-webdav)
internal/carddav  → backend CardDAV (go-webdav)
web/              → React 18 + Vite + Tailwind v4 + FullCalendar + tsdav + ical.js
deploy/           → overlay docker-compose para opencloud-compose
```

## Desarrollo local

Terminal 1 (backend, modo dev con usuario forzado):

```bash
go mod tidy
go run ./cmd/server -addr :5232 -data-dir ./data -dev-user alice
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
3. `docker compose up -d` — Radicale desaparece, davcloud ocupa su lugar con
   los mismos datos.

Los clientes (DAVx⁵, iOS, GNOME, Thunderbird...) se configuran exactamente
igual que con Radicale: URL base `https://<tu-dominio>/caldav` y
`.../carddav`, usuario + app token. La web UI vive en el propio OpenCloud
bajo `/` del servicio... (sirve el binario en la raíz; ajusta el proxy si
quieres una ruta propia, p.ej. `/dav`).

## Estado: esqueleto funcional, no producto

Funciona: sync completo con DAVx⁵/Thunderbird/Evolution, CRUD de eventos y
contactos, calendarios múltiples, ETags/optimistic locking, creación
automática de colecciones, drop-in sobre datos Radicale existentes.

Pendiente (en orden de dolor):
- [ ] Expansion de RRULE y time-range queries **en el servidor** (hoy el
      filtro es conservador y los clientes expanden en local)
- [ ] Edición de recurrencias en la web UI (hoy editar normaliza a único)
- [ ] Compartición entre usuarios de OpenCloud (requiere ACL propias; el
      proxy solo inyecta identidad, no grupos)
- [ ] Invitaciones iMIP (enviar/recibir .ics por email)
- [ ] Tests contra clientes reales (DAVx⁵ suite, iOS, GNOME Online Accounts)
- [x] API verificada contra go-webdav v0.7 (pkg.go.dev): Handler{Backend,
      Prefix}, PutCalendarObject/PutAddressObject unificados, caldav.Filter/
      carddav.Filter para queries. Pendiente: honrar If-Match/If-None-Match
      de *PutCalendarObjectOptions

## Licencia

MIT (sugerida). go-webdav es MIT; FullCalendar MIT; ical.js MPL-2.0.

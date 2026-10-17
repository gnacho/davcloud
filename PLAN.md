# PLAN - davcloud: de esqueleto a reemplazo de Radicale

Objetivo: sustituir Radicale (binario + systemd en el CT) por davcloud sin
que DAVx⁵, Thunderbird, iOS ni las extensiones web (opencloud-agenda,
opencloud-contacts) noten la diferencia.

Estado al 17-Oct-2026: **fases 0-3 completadas** (issues #1-#9 cerrados) y
**nada de Docker**: el despliegue es un binario estático + systemd (ver
`deploy/README.md`).

## Hecho

- **Fase 0 - compila y responde** (PR #2): `go.mod`/`go.sum` contra versiones
  reales, API v0.7 correcta (`webdav.NewHTTPError`, `AddressObject.Card`,
  `Put*Object` con precondiciones), PROPFIND bien formado, PUT/GET/DELETE.
- **Fase 1 - robustez y paridad del layout** (PR #2): escrituras atómicas
  (temp + fsync + rename), `If-Match`/`If-None-Match` conformes (412), ETag
  canónico, y `internal/dav`: handler propio con el layout plano de Radicale
  (go-webdav deduce el tipo por número de segmentos y espera un nivel más).
- **PROPPATCH** (PR #10): rename, `calendar-color`, props de cliente con las
  claves canónicas de Radicale, atomicidad RFC 4918.
- **Items byte a byte** (PR #11): PUT/GET propios; el disco queda idéntico a
  lo que envía el cliente (como hace Radicale); UID = nombre del recurso;
  Content-Type validado por parseo, no por cabecera.
- **sync-collection** (PR #12): RFC 6578 con la semántica honesta del
  multifilesystem (token = ctag; token viejo/vacío → full sync).
- **Recurrencias** (PR #13): caracterización: go-webdav YA expande
  RRULE/RDATE/EXDATE en los time-ranges; tolerancia a RRULE rotas (fallback a
  evento simple) y TZID sin VTIMEZONE verificado.
- **Contrato real de producción** (PR #15): verificado read-only contra el CT:
  el proxy elimina el prefijo y manda `X-Script-Name` (→ modo `-dav-root`);
  el `.Radicale.props` real guarda el component-set como string (→ formato
  compatible en ambos sentidos).
- **Extras**: CI de 4 jobs (PR #25), binario versionado (PR #26), README
  bilingüe (PR #17), licencia AGPL-3.0 (PR #14), sin Docker (PR #27).
- **Ensayo de migración en seco**: copia real del volumen del CT montada en
  davcloud: lectura, escritura byte a byte y sync verificados con dos
  usuarios reales. El CT quedó intacto.

## Pendiente (issues)

- **#21 - Fase 4: migración de producción** (swap systemd en el CT 4125
  según `deploy/README.md`; backup primero; verificación con la UI y DAVx⁵).
- **#22 - Fase 5: soak + drill de rollback** (1-2 semanas con davcloud como
  único servidor DAV; después arrancar Radicale sobre el mismo volumen y
  verificar que lee todo lo escrito por davcloud; solo entonces retirar
  Radicale).
- **#23, #24 - Web UI menores**: vCard 3.0 en vez de 4.0 y tests de los
  helpers (la edición de recurrencias en la UI sigue siendo un TODO
  documentado en `web/src/lib/ics.ts`).
- **Fase 6 - paridad extendida (opcional, por necesidad)**: iMIP
  (invitaciones por email) y compartición de calendarios (requiere ACLs que
  el proxy no proporciona). Radicale tampoco cubre bien ninguno de los dos.
- Publicar o no el repo en GitHub: decisión aparte (hoy privado).

## Regla de oro

Cada fase termina con un criterio de salida observable. Nada de "debería
funcionar": o DAVx⁵ lo muestra, o la fase no ha terminado.

# PLAN - davcloud: de esqueleto a reemplazo de Radicale

Objetivo: sustituir Radicale (binario + systemd en un contenedor) por davcloud
sin que DAVx⁵, Thunderbird, iOS ni las extensiones web (opencloud-agenda,
opencloud-contacts) noten la diferencia.

Estado al 18-Oct-2026: **fases 0-4 completadas** (issues #1-#9 y #21 cerrados;
davcloud es el servidor DAV de producción desde el 17-Oct) y **fase 5 en curso**
(soak en marcha; el **drill de rollback pasó el 18-Oct-2026**, issue #22).
**Nada de Docker**: el despliegue es un binario estático + systemd (ver
`deploy/README.md`). Repo público desde el 18-Oct-2026, licencia AGPL-3.0.

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
- **Contrato real de producción** (PR #15): verificado read-only contra el
  despliegue real: el proxy elimina el prefijo y manda `X-Script-Name`
  (→ modo `-dav-root`); el `.Radicale.props` real guarda el component-set
  como string (→ formato compatible en ambos sentidos).
- **Extras**: CI de 4 jobs (PR #25), binario versionado (PR #26), README
  bilingüe (PR #17), licencia AGPL-3.0 (PR #14), sin Docker (PR #27).
- **Fase 4 - migración de producción** (issue #21, 17-Oct-2026): swap de
  systemd en el contenedor según `deploy/README.md`, con backup previo,
  verificado con la UI y con DAV por curl (PROPFIND con hrefs prefijados,
  sync-collection, ciclo PUT/GET/DELETE). Radicale queda parado y deshabilitado.
- **Web UI**: vCard 3.0 al escribir y tests de los helpers (PR #30), vista de
  tarjetas y arreglos de la libreta (PRs #21, #33).
- **Ensayo de migración en seco**: copia real del volumen de producción
  montada en davcloud: lectura, escritura byte a byte y sync verificados con
  dos usuarios reales. El original quedó intacto.
- **Drill de rollback** (issue #22, 18-Oct-2026, sobre el volumen de
  producción): Radicale 3.5.3 arrancado sobre los mismos datos, con los 5 items
  servidos con el mismo UID y el mismo conjunto de campos, las props de
  colección leídas y ninguno de los items borrados reapareciendo; vuelta a
  davcloud con los items byte a byte intactos (Radicale solo añade su
  `.Radicale.cache/item/`).

## Pendiente (issues)

- **#22 - Fase 5: soak + drill de rollback** (soak en curso desde el 17-Oct:
  davcloud es el único servidor DAV; el drill ya pasó el 18-Oct-2026, ver
  arriba). Queda el uso diario con datos reales y, solo entonces, retirar
  Radicale del contenedor.
- **Edición de recurrencias en la web UI**: sigue siendo un TODO documentado
  en `web/src/lib/ics.ts`.
- **Fase 6 - paridad extendida (opcional, por necesidad)**: iMIP
  (invitaciones por email) y compartición de calendarios (requiere ACLs que
  el proxy no proporciona). Radicale tampoco cubre bien ninguno de los dos.

## Regla de oro

Cada fase termina con un criterio de salida observable. Nada de "debería
funcionar": o DAVx⁵ lo muestra, o la fase no ha terminado.

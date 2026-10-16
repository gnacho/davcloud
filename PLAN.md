# PLAN - davcloud: de esqueleto a reemplazo de Radicale

Objetivo: que `docker compose` pueda sustituir `radicale/radicale.yml` por
`deploy/opencloud-davcloud.yml` sin que DAVx⁵, Thunderbird, iOS ni las
extensiones web (opencloud-agenda, opencloud-contacts) noten la diferencia.

## Puntos a VERIFICAR primero (bloquean decisiones posteriores)

1. **API v0.7 exacta**: firmas de `PutCalendarObjectOptions` /
   `PutAddressObjectOptions` (campos If-Match / If-None-Match) en
   pkg.go.dev/github.com/emersion/go-webdav.
2. **RecurrenceSet en go-ical**: emersion/go-ical tiene `ical.RecurrenceSet`
   con método `Between(start, end)` que expande RRULE/RDATE/EXDATE. Si existe,
   la expansión server-side es casi gratis; si no, evaluar `rrule-go`.
3. **sync-collection**: comprobar si go-webdav v0.7 soporta REPORT
   sync-collection (backend de sync incremental). Si no: los clientes hacen
   fallback a listado completo - aceptable a escala personal, pero hay que
   saberlo antes de prometer paridad.
4. **Qué inyecta el proxy de OpenCloud como X-Remote-User**: UUID (según
   README de opencloud-agenda) vs username. El esqueleto lo trata opaco, pero
   condiciona el test de migración con un volumen Radicale real.

## Estado al 16-Oct-2026 (fases 0+1 hechas y verificadas)

- Verificación 1 (API v0.7): resuelta. El drift era pequeño
  (`webdav.NewHTTPError`, `AddressObject.Card`, `vcard.ToV4` sin retorno,
  `RecurrenceSet` existe como método de componente devolviendo
  `rrule.Set`). Todo corregido.
- Verificación 3 (sync-collection): NO hay lado servidor en go-webdav.
  Decisión: no anunciar `sync-token` y dejar el fallback
  PROPFIND + getetag; implementarlo propio queda como fase 3.
- Verificación 4 (X-Remote-User): es el UUID del usuario.
- **Gap encontrado y resuelto**: go-webdav deduce el tipo de recurso por
  número de segmentos y espera un nivel más que Radicale. `internal/dav`
  implementa PROPFIND/MKCOL/DELETE/OPTIONS/.well-known con el layout plano
  y delega REPORT/GET/HEAD/PUT en la librería.
- Fase 0: OK (`go build`, `go vet`, `gofmt` limpios; PROPFIND 207,
  PUT/GET/DELETE con `curl`).
- Fase 1: OK (escrituras atómicas, condicionales 412, ETag canónico, 21
  tests en verde, prueba de humo completa con el binario).
- Gaps medidos en la fase 1: PROPPATCH 501, sync-collection 400, PUT
  exige Content-Type calendar/vcard, go-ical/go-vcard reordenan
  propiedades al re-encodear.

## Fase 0 - Compilación y smoke test (día 1)

- `go mod tidy && go build ./... && go vet ./...`; corregir drift de firmas.
- Arrancar con `-dev-user alice`, `curl -X PROPFIND` contra /caldav/ y
  /carddav/ (207 multistatus esperado), PUT/GET de un .ics de prueba.
- **Criterio de salida**: el binario compila y responde PROPFIND bien formado.

## Fase 1 - Robustez del storage (semana 1)

- Escrituras atómicas: temp file + rename (DAVx⁵ sincroniza colecciones en
  paralelo; una escritura a medias corrompe el item).
- Honrar condicionales: If-None-Match: * (409 si existe) e If-Match (412 si
  el ETag no cuadra) en PutCalendarObject/PutAddressObject.
- Tests unitarios del store: layout, .Radicale.props, ETags, cleanSeg
  (path traversal), EnsureUser idempotente.
- **Criterio de salida**: tests en verde + PUT condicional conforme.

## Fase 2 - Queries con recurrencias (semanas 2–4; el trabajo real)

- Test de caracterización: evento con RRULE semanal y QUERY time-range que
  corta la serie → ¿qué devuelve `caldav.Filter` hoy? (hipótesis: el master
  sin expandir; resultado correcto-pero-incompleto).
- Implementar expansión server-side usando `ical.RecurrenceSet.Between` (o
  alternativa) dentro de QueryCalendarObjects: un evento recurrente que cruza
  el rango debe aparecer aunque su DTSTART esté fuera.
- Lo mismo, a su nivel, para QueryAddressObjects si el filtro deja títeres.
- **Criterio de salida**: DAVx⁵ muestra correctamente eventos recurrentes
  que empiezan antes de la ventana de sync.

## Fase 3 - Sync-collection (semana 5, según verificación 3)

- Si go-webdav no lo trae: decisión documentada (contribuir upstream vs
  aceptar fallback) y métrica: tamaño de PROPFIND completo con X años de
  calendario. Si es <1MB, el fallback es viable para uso personal.

## Fase 4 - Integración OpenCloud (semana 6)

- Overlay en opencloud-compose real; app token desde la UI; verificar que
  agenda y contacts (frank) cargan contra davcloud.
- **Test de migración**: copia del volumen radicale-data real → montar en
  davcloud → diff de colecciones/items visibles en la UI y en DAVx⁵.
- DAVx⁵ en el móvil real: sync bidireccional (crear en móvil, aparece en
  web, y viceversa).
- **Criterio de salida**: un día de uso real sin discrepancias. Aquí se
  puede quitar Radicale... pero solo después de la fase 5.

## Fase 5 - Soak y rollback drill (semana 7)

- Uso diario 1–2 semanas con ambos montados (davcloud como primario).
- Drill de rollback: volver a radicale.yml, confirmar que Radicale lee el
  volumen tras las escrituras de davcloud (layout compatible = debería).
- Backup/restauración del volumen documentados.

## Fase 6 - Paridad extendida (opcional, por necesidad)

En orden de valor real (Radicale tampoco cubre bien los tres primeros):
1. Free/busy REPORT + iMIP (invitaciones) - solo si agendáis reuniones.
2. Compartición de calendarios entre usuarios (fichero de rights estilo
   Radicale o ACLs).
3. Web UI propia: probablemente **no** - las extensiones de frank cubren
   esto mejor. Valorar deprecar el frontend del esqueleto y mantener solo
   el servidor.

## Costes estimados (con asistencia IA)

| Fase | Esfuerzo | Riesgo |
|---|---|---|
| 0 | 1 día | drift de API |
| 1 | 2–4 días | bajo |
| 2 | 1–3 semanas | recurrencias = zona pantanosa universal |
| 3 | 0–1 semana | depende de verificación 3 |
| 4 | 2–4 días | sorpresas de auth/UUID |
| 5 | 1–2 semanas (pasiva) | bajo |
| 6 | a voluntad | - |

## Regla de oro

Cada fase termina con un criterio de salida observable. Nada de "debería
funcionar": o DAVx⁵ lo muestra, o la fase no ha terminado.

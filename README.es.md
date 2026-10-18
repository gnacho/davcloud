# davcloud

[![ci](https://github.com/gnacho/davcloud/actions/workflows/ci.yml/badge.svg)](https://github.com/gnacho/davcloud/actions/workflows/ci.yml)

[English](README.md) | [Español](README.es.md)

Un **servidor CalDAV + CardDAV en Go**, de binario único, construido como
reemplazo drop-in de [Radicale](https://radicale.org/): habla las mismas
URLs, lee y escribe el mismo layout de datos en disco y obtiene los usuarios
de la misma cabecera que inyecta un proxy inverso. Pensado para colocarse
detrás de un despliegue de
[OpenCloud](https://github.com/opencloud-eu/opencloud) (su despliegue de
referencia trae Radicale exactamente así), pero funciona detrás de cualquier
proxy que autentique e inyecte la identidad.

Un binario estático (~7 MB), sin dependencias de runtime, sin base de datos,
sin contenedores: el servidor y el volumen de datos son todo el despliegue.

## Qué hace

- **CalDAV**: descubrimiento de calendarios, REPORTs `calendar-query` y
  `calendar-multiget` (con expansión server-side de RRULE/RDATE/EXDATE en
  las queries de rango), `sync-collection` (RFC 6578) para sincronización
  incremental, MKCALENDAR, free-busy.
- **CardDAV**: descubrimiento de libretas, REPORTs `addressbook-query` y
  `addressbook-multiget`, MKCOL, el mismo endpoint de sync.
- **Plomería DAV común**: PROPFIND (Depth 0/1/infinity, `prop`/`allprop`/
  `propname`, propstat 404 para propiedades desconocidas), PROPPATCH con
  atomicidad RFC 4918 (renombrar, `calendar-color`, descripciones), PUT/GET
  condicionales (`If-Match`, `If-None-Match`, 304), DELETE, MKCOL/MKCALENDAR,
  OPTIONS y descubrimiento `/.well-known/caldav|carddav`.
- **Multiusuario, auth agnóstica del proxy**: cada petición lleva
  `X-Remote-User`; el servidor nunca pide contraseñas. Emparejalo con
  cualquier proxy inverso que autentique (el proxy de OpenCloud, Authelia,
  basic auth...).
- **Bootstrap automático por usuario**: el primer acceso crea "Personal
  Calendar" y "Personal Address Book" del usuario, como las colecciones
  predefinidas de Radicale.
- Además, una web UI opcional integrada (React + FullCalendar) para uso
  standalone, servida por el mismo binario.

Lo que deliberadamente **no** hace (todavía): invitaciones (iMIP),
compartición de calendarios entre usuarios y edición de recurrencias en la
web UI.

## Cómo lo hace

```
clientes (DAVx5, Thunderbird, iOS, apps web)
      |  /caldav/*  /carddav/*
      v
proxy inverso con auth  (autentica -> X-Remote-User: <id usuario>)
      |  quita el prefijo, pone X-Script-Name
      v
davcloud  127.0.0.1:5232  (un binario estático)
      |
      v
volumen de datos, layout "multifilesystem" de Radicale

<volumen>/collection-root/<usuario>/<coleccion>/.Radicale.props
<volumen>/collection-root/<usuario>/<coleccion>/<item>.ics|.vcf
```

- **URLs planas, compatibles con Radicale**:
  `/caldav/<usuario>/<coleccion>/<item>`. La mayor parte de la plomería DAV
  vive en `internal/dav`, escrita para exactamente ese layout. `go-webdav`
  (la librería) se sigue usando donde brilla: el motor de filtros de
  `calendar-query`/`multiget`, que incluso expande recurrencias.
- **Almacenamiento byte a byte**: los items se guardan exactamente como los
  envió el cliente y se sirven sin cambios, así que un volumen escrito por
  davcloud es indistinguible de uno escrito por Radicale, en ambos sentidos.
  Los metadatos de colección viven en los `.Radicale.props` propios de
  Radicale (mismas claves, incluido el string separado por comas del
  supported-calendar-component-set).
- **Los nombres que gestiona el servidor están reservados**: un nombre que
  empieza por punto (el `.Radicale.props` propio de cada colección, los ficheros
  `.tmp-*` de las escrituras atómicas) o que acaba en tilde no existe para los
  clientes. Un `PUT` sobre ese nombre responde 400 y `DELETE`/`GET` responden
  404, así que ningún cliente puede romper una colección escribiendo encima de
  sus metadatos o borrándolos. Radicale aplica la misma regla con
  `pathutils.is_safe_filesystem_path_component`.
- **La identidad es una cabecera**: se confía ciegamente en
  `X-Remote-User`, así que el puerto nunca debe ser alcanzable sin el proxy
  autenticador delante. En modo `-dav-root` el proxy elimina los prefijos
  `/caldav`/`/carddav` y marca el endpoint con `X-Script-Name`; davcloud
  sigue emitiendo hrefs prefijados, igual que hace Radicale.
- **Una base de código, dos roles**: el mismo binario sirve la API DAV y
  (en modo standalone) una pequeña web UI en React. Detrás de OpenCloud las
  apps web son la UI, así que davcloud corre con `-dav-root` y no la sirve.

## Cómo instalarlo

Requisitos: Go 1.24+ para compilar. `npm` solo si quieres la web UI. Sin
base de datos ni dependencias de runtime.

**Compilar**

```bash
git clone https://github.com/gnacho/davcloud && cd davcloud
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --tags --always) -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o davcloud ./cmd/server
./davcloud -version
```

**Probarlo standalone** (identidad simulada, sin proxy):

```bash
./davcloud -addr 127.0.0.1:5232 -data-dir ./data -dev-user alice
# en otra terminal: cualquier cliente CalDAV contra http://127.0.0.1:5232/caldav/
```

**Producción detrás de OpenCloud** (el camino probado; runbook completo con
backup y rollback en [deploy/README.md](deploy/README.md)):

1. Haz backup del directorio de datos de Radicale y cambia el servicio
   manteniendo el mismo puerto:

   ```ini
   # /etc/systemd/system/davcloud.service (extracto)
   [Service]
   ExecStart=/usr/local/bin/davcloud -addr 127.0.0.1:5232 -data-dir /var/lib/radicale/collections -dav-root
   User=radicale
   ReadWritePaths=/var/lib/radicale/
   ```

2. Asegúrate de que las rutas del proxy apuntan a él e inyectan la identidad
   (esto es lo que es un `proxy.yaml` de OpenCloud con Radicale, y davcloud
   usa exactamente la misma forma):

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

3. Verifica (como lo llamaría el proxy, con tu id de usuario real):

   ```bash
   curl -s -X PROPFIND -H 'Depth: 0' \
     -H 'X-Script-Name: /caldav' -H 'X-Remote-User: <id-usuario>' \
     http://127.0.0.1:5232/ | grep -o '<D:href>[^<]*</D:href>'
   # /caldav/<id-usuario>/
   ```

El rollback es un cambio de servicio: para davcloud y arranca Radicale sobre
el mismo volumen (layout y metadatos son byte-compatibles en ambos sentidos).

**Límites** (los valores por defecto están elegidos para coincidir con la
configuración de Radicale):

| Flag | Por defecto | Qué limita |
|---|---|---|
| `-max-request-size` | 100 MiB | el cuerpo de cualquier petición; por encima responde 413 |
| `-max-item-size` | 10 MiB | un objeto de calendario o una vCard, anunciado al cliente como `max-resource-size` |
| `-max-expand` | 10000 | las instancias de recurrencia que un objeto puede expandir al resolver una consulta por rango; por encima, el objeto se compara por sus fechas simples |

Cada uno se puede fijar también por entorno (`DAVCLOUD_MAX_REQUEST_SIZE`,
`DAVCLOUD_MAX_ITEM_SIZE`, `DAVCLOUD_MAX_EXPAND`). El servidor HTTP se construye
con timeouts explícitos (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`,
`IdleTimeout`, `MaxHeaderBytes`) en vez de con los valores por defecto de Go,
que no pone ninguno.

## Estado

En uso diario como servidor DAV detrás del despliegue de OpenCloud del autor. El
drill de rollback pasó el 18-Oct-2026: Radicale 3.5.3 sirvió el mismo volumen
otra vez, leyó todo lo escrito por davcloud (mismos UID y campos, mismas props
de colección) y no tocó los ficheros, así que volver atrás es un cambio de
servicio. El periodo de soak sigue en marcha, así que es software en desarrollo
activo. La lista de fases y lo que queda está en
[PLAN.md](PLAN.md). Cubierto por 40 tests de Go y 10 de la web, CI en cada
push (test/lint/security/web) y un ensayo de migración contra una copia de un
volumen real de producción. Las limitaciones conocidas están seguidas como
issues: iMIP, compartición de calendarios, edición de recurrencias en la web.

## Licencia

AGPL-3.0 (ver LICENSE). Dependencias: go-webdav MIT, go-ical MIT, go-vcard
MIT, FullCalendar MIT, ical.js MPL-2.0.

# Despliegue de davcloud en sustitución de Radicale

davcloud es un binario estático de Go: no hay contenedores por medio. El
despliegue real (CT 4125, nodo4) sustituye el servicio systemd de Radicale por
uno propio, sobre el MISMO puerto y el MISMO volumen, así que el proxy de
OpenCloud no se toca.

Contrato que el proxy ya cumple y que NO hay que cambiar (verificado en
producción, `proxy.yaml`): reenvía `/caldav/` y `/carddav/` a
`127.0.0.1:5232` con el prefijo **eliminado del path**, cabecera
`X-Script-Name: /caldav|/carddav` y `X-Remote-User` con el id opaco del
usuario. Por eso davcloud arranca con `-dav-root`.

## 0. Backup (obligatorio)

El CT está fuera de PBS: copia fría del volumen antes de nada.

```
pct exec 4125 -- sh -c 'systemctl stop radicale && cp -a /var/lib/radicale /var/lib/radicale.bak-$(date +%Y%m%d) && systemctl start radicale'
```

## 1. Binario estático

En cualquier máquina con Go (el CT no necesita toolchain):

```
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --tags --always) -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o davcloud ./cmd/server
./davcloud -version
sha256sum davcloud
```

Subida al CT (el `scp` del binario en marcha da "Text file busy": parar el
servicio antes del `mv`, como en ocapps):

```
scp -P 2222 davcloud root@nodo4.domatix.com:/root/davcloud-new
pct push 4125 /root/davcloud-new /usr/local/bin/davcloud --perms 755
```

## 2. Web UI compilada (opcional)

En modo `-dav-root` la SPA no se sirve (todas las rutas son DAV), así que
este paso es solo si quieres conservar la UI para uso directo:

```
cd web && npm ci && npm run build
pct push 4125 web/dist root@nodo4.domatix.com... # o copiala fuera si no la vas a usar
```

## 3. Unit systemd (mismo puerto, mismo volumen, mismo usuario)

`/etc/systemd/system/davcloud.service`. Usa el usuario `radicale` porque es el
dueño del volumen (`/var/lib/radicale/collections` es `radicale:radicale 750`)
y el mismo hardening que el unit de Radicale:

```
[Unit]
Description=davcloud CalDAV/CardDAV server (Radicale drop-in)
After=network.target
Requires=network.target

[Service]
ExecStart=/usr/local/bin/davcloud -addr 127.0.0.1:5232 -data-dir /var/lib/radicale/collections -dav-root
Restart=on-failure
LogsDirectory=davcloud
User=radicale
UMask=0027
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
NoNewPrivileges=true
ReadWritePaths=/var/lib/radicale/

[Install]
WantedBy=multi-user.target
```

Sin `-dev-user` en producción: la identidad SIEMPRE la inyecta el proxy.

## 4. Swap (transparente para el proxy)

```
pct exec 4125 -- sh -c 'systemctl stop radicale && systemctl disable radicale && systemctl daemon-reload && systemctl enable --now davcloud'
```

## 5. Verificación (dentro del CT, sin tocar el proxy)

Con `X-Script-Name` y `X-Remote-User` de un usuario real (el id opaco se
saca de la carpeta: `ls /var/lib/radicale/collections/collection-root/`):

```
curl -s -X PROPFIND -H 'Depth: 0' \
  -H 'X-Script-Name: /caldav' -H 'X-Remote-User: <uuid>' \
  http://127.0.0.1:5232/ | grep -o '<D:href>[^<]*</D:href>'
# esperado: /caldav/<uuid>/

curl -s -X REPORT -H 'X-Script-Name: /caldav' -H 'X-Remote-User: <uuid>' \
  -H 'Content-Type: application/xml' \
  --data-binary '<D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:prop><D:getetag/></D:prop></D:sync-collection>' \
  http://127.0.0.1:5232/<uuid>/def-calendar/ -o /dev/null -w '%{http_code}\n'
# esperado: 207
```

Y desde fuera (navegador): abrir la app agenda de OpenCloud y comprobar que
los calendarios y eventos reales aparecen. **No hacer `curl` sin auth contra
`drive.domatix.cloud`**: el fail2ban del NPM banea la IP de casa por 401
repetidos (lección del 13-Sep-2026).

## 6. Rollback drill (fase 5 del PLAN)

El volumen es byte-compatible en ambos sentidos (davcloud guarda los items
tal cual y las props de colección en el mismo formato de Radicale):

```
pct exec 4125 -- sh -c 'systemctl stop davcloud && systemctl start radicale'
```

Después de un periodo con davcloud, arrancar Radicale y comprobar que ve los
calendarios y eventos escritos por davcloud (mismos items, mismo layout, y el
component-set en formato string). Solo cuando este drill pase se puede dar el
cambio por definitivo.

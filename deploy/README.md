# Despliegue de davcloud en sustitución de Radicale

davcloud es un binario estático de Go: no hay contenedores por medio. El
despliegue de referencia (un contenedor LXC sobre Proxmox, en nuestro caso)
sustituye el servicio systemd de Radicale por uno propio, sobre el MISMO
puerto y el MISMO volumen, así que el proxy de OpenCloud no se toca.

Contrato que el proxy ya cumple y que NO hay que cambiar (verificado en
producción, `proxy.yaml`): reenvía `/caldav/` y `/carddav/` a
`127.0.0.1:5232` con el prefijo **eliminado del path**, cabecera
`X-Script-Name: /caldav|/carddav` y `X-Remote-User` con el id opaco del
usuario. Por eso davcloud arranca con `-dav-root`.

## 0. Backup (obligatorio)

El volumen entero es `/var/lib/radicale` (dentro vive `collections/`). Copia
antes de nada: si el contenedor no está cubierto por backups automáticos, es
la única red de seguridad.

Copia con el servicio parado (fría):

```
pct exec <ctid> -- sh -c 'systemctl stop radicale && cp -a /var/lib/radicale /var/lib/radicale.bak-$(date +%Y%m%d) && systemctl start radicale'
```

Copia con el servicio en marcha (en caliente): solo vale si luego **verificas
un manifiesto**, porque un fichero puede copiarse a medias. Con el escritor DAV
parado en la práctica (nadie escribiendo en ese momento) la copia en caliente
es segura, pero la comprobación es la que lo demuestra:

```
pct exec <ctid> -- bash -s <<'EOF'
cd /var/lib/radicale
find collections -type f -print0 | xargs -0 sha256sum | LC_ALL=C sort > /tmp/manifest-src.txt
DEST=/var/lib/radicale.bak-$(date +%Y%m%d-%H%M%S)
cp -a /var/lib/radicale "$DEST"
cd "$DEST" && find collections -type f -print0 | xargs -0 sha256sum | LC_ALL=C sort > /tmp/manifest-bak.txt
diff /tmp/manifest-src.txt /tmp/manifest-bak.txt && echo "COPIA IDENTICA: OK"
EOF
```

Dos detalles que cuestan tiempo si no se saben:

- Ordena con `LC_ALL=C` en AMBOS lados. El orden de `sort` cambia con el
  locale y un diff entre dos manifiestos equivalentes te hará perseguir una
  diferencia que no existe.
- Los hashes no dicen nada del propietario: al restaurar hay que reponer
  `radicale:radicale` (el unit corre como `radicale`).

Copia **fuera** del contenedor (recomendado: es la que sobrevive a perderlo):

```
pct exec <ctid> -- tar czf /root/radicale-vol-$(date +%Y%m%d-%H%M%S).tgz -C /var/lib radicale
pct exec <ctid> -- cat /root/radicale-vol-<stamp>.tgz > ./radicale-vol-<stamp>.tgz
sha256sum ./radicale-vol-<stamp>.tgz    # compara con el sha256 sacado dentro del CT
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
scp davcloud root@<your-host>:/root/davcloud-new   # -P <port> si tu SSH no es el 22
pct push <ctid> /root/davcloud-new /usr/local/bin/davcloud --perms 755
```

## 2. Web UI compilada (opcional)

En modo `-dav-root` la SPA no se sirve (todas las rutas son DAV), así que
este paso es solo si quieres conservar la UI para uso directo:

```
cd web && npm ci && npm run build
pct push <ctid> web/dist /srv/davcloud-web --recursive
```

Si la sirves, apunta el binario a ese directorio con `-ui-dir /srv/davcloud-web`.

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
pct exec <ctid> -- sh -c 'systemctl stop radicale && systemctl disable radicale && systemctl daemon-reload && systemctl enable --now davcloud'
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
los calendarios y eventos reales aparecen. **No hagas `curl` sin autenticar
contra el hostname público para "ver si responde"**: si delante hay un
fail2ban, una racha de 401 te banea la IP y te deja fuera del servicio (nos
pasó: el corte alcanza a toda la red de la que salgas).

## 6. Rollback drill (fase 5 del PLAN)

El volumen es byte-compatible en ambos sentidos (davcloud guarda los items
tal cual y las props de colección en el mismo formato de Radicale):

```
pct exec <ctid> -- sh -c 'systemctl stop davcloud && systemctl start radicale'
```

Después de un periodo con davcloud, arrancar Radicale y comprobar que ve los
calendarios y eventos escritos por davcloud (mismos items, mismo layout, y el
component-set en formato string). Solo cuando este drill pase se puede dar el
cambio por definitivo.

Ejecutado y pasado el 18-Oct-2026 sobre el volumen de producción: Radicale 3.5.3
arrancó sobre los mismos datos y sirvió los 5 items con el mismo UID y el mismo
conjunto de campos, incluidas las props de colección; ninguno de los items
borrados durante la fase con davcloud reapareció. La vuelta deja los items byte
a byte intactos: lo único que añade Radicale es su caché
`.Radicale.cache/item/`, que davcloud ignora.

Dos trampas al verificar, medidas ese día:

- Comparar los items **en disco**, no en la respuesta DAV. Radicale re-serializa
  al servir (propiedades en orden alfabético y un CRLF final en las vCards:
  187 -> 189 bytes) aunque no modifica los ficheros. davcloud sí sirve los items
  byte a byte.
- davcloud elige el endpoint por `X-Script-Name`: pedir un `.ics` con
  `X-Script-Name: /carddav` devuelve 404 `dav: resource not found`, y el hash
  del cuerpo del error parece una diferencia de datos. Usar la cabecera del tipo
  correcto.

## 7. Restore del volumen (verificado)

Procedimiento comprobado: extraer la copia, reponer propietario, comparar el
manifiesto con el del volumen original y (si quieres) arrancar un davcloud
aparte sobre la copia para verla servida antes de tocar producción.

```
pct exec <ctid> -- bash -s <<'EOF'
set -e
TMP=/tmp/restore-check
rm -rf $TMP && mkdir -p $TMP
tar xzf /root/radicale-vol-<stamp>.tgz -C $TMP
chown -R radicale:radicale $TMP/radicale
cd $TMP/radicale
find collections -type f -print0 | xargs -0 sha256sum | LC_ALL=C sort | sha256sum
# compara este hash con el del manifiesto del volumen de origen
EOF
```

Servirlo en un puerto aparte, sin tocar el de producción:

```
pct exec <ctid> -- bash -c 'timeout 30 runuser -u radicale -- /usr/local/bin/davcloud \
  -addr 127.0.0.1:15233 -data-dir /tmp/restore-check/radicale/collections -dav-root &' 
pct exec <ctid> -- curl -s -X PROPFIND -H 'Depth: 1' -H 'X-Script-Name: /carddav' \
  -H 'X-Remote-User: <uuid>' http://127.0.0.1:15233/<uuid>/def-addressbook/
```

Ojo al comprobar: un PROPFIND con un `X-Remote-User` que no exista en el
volumen **crea** sus colecciones vacías (bootstrap automático), así que el
manifiesto cambia por tu culpa. Compara el manifiesto justo después de
extraer, antes de arrancar nada.

Restaurar SOBRE el volumen en uso:

```
pct exec <ctid> -- sh -c 'systemctl stop davcloud \
  && mv /var/lib/radicale /var/lib/radicale.roto-$(date +%Y%m%d-%H%M%S) \
  && tar xzf /root/radicale-vol-<stamp>.tgz -C /var/lib \
  && chown -R radicale:radicale /var/lib/radicale \
  && systemctl start davcloud'
```

Este último paso es el único del runbook que no se ha ejercitado todavía: la
copia y la verificación sí, y el arranque sobre la copia también, pero el
reemplazo del volumen en producción no se ha hecho nunca (no ha hecho falta).
Se prueba en el drill de la fase 5, o antes si surge la necesidad.

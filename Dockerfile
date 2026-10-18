# ── Frontend build ──────────────────────────────────────────────────────────
FROM node:22-alpine AS ui
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
RUN npm run build

# ── Backend build ───────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS build
# Version metadata as build args, so the binary inside the image reports the
# tag it was built from instead of "dev".
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/davcloud ./cmd/server

# ── Runtime ─────────────────────────────────────────────────────────────────
FROM alpine:3.21
ARG VERSION=dev
LABEL org.opencontainers.image.title="davcloud" \
      org.opencontainers.image.description="CalDAV and CardDAV server over a Radicale-compatible data directory" \
      org.opencontainers.image.source="https://github.com/gnacho/davcloud" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/davcloud /usr/local/bin/davcloud
COPY --from=ui /src/dist /app/ui
ENV DAVCLOUD_ADDR=:5232     DAVCLOUD_DATA=/data
VOLUME /data
EXPOSE 5232
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/davcloud"]
CMD ["-addr", ":5232", "-data-dir", "/data", "-ui-dir", "/app/ui"]

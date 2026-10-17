# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
RUN npm install --global pnpm@10.32.1
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
# Go 版本以 go.mod 的 toolchain 行为准，镜像自带的版本较旧时自动下载
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG REVISION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -p 4 -trimpath \
    -ldflags "-s -w -X main.version=$VERSION -X main.revision=$REVISION" -o /out/tillbox ./cmd/tillbox

FROM alpine:3.24
ARG VERSION=dev
ARG REVISION=dev
LABEL org.opencontainers.image.title="Tillbox" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION
RUN apk add --no-cache ca-certificates tzdata sqlite \
    && addgroup -S -g 10001 tillbox \
    && adduser -S -D -H -u 10001 -G tillbox tillbox \
    && mkdir -p /data && chown tillbox:tillbox /data

COPY --from=build /out/tillbox /usr/local/bin/tillbox
ENV TILLBOX_DATA_DIR=/data TILLBOX_LISTEN=:8080
USER tillbox
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/tillbox"]
CMD ["serve"]

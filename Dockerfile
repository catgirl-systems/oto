# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG BUILD_DATE
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/oto ./cmd/oto && \
    printf 'oto version: %s\nBuild-date: %s\n' "$VERSION" "$BUILD_DATE" > /out/build_version

FROM ghcr.io/linuxserver/baseimage-alpine:3.23

ARG BUILD_DATE
ARG REVISION
ARG VERSION=dev

LABEL org.opencontainers.image.created="$BUILD_DATE" \
      org.opencontainers.image.description="A Linux Soulseek client running the oto daemon" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.source="https://github.com/catgirl-systems/oto" \
      org.opencontainers.image.title="oto" \
      org.opencontainers.image.version="$VERSION"

ENV HOME=/config \
    LSIO_FIRST_PARTY=false \
    XDG_STATE_HOME=/config

COPY --from=build /out/oto /usr/local/bin/oto
COPY --from=build /out/build_version /build_version
COPY config.example.json /defaults/config.json
COPY root/ /

EXPOSE 50300/tcp
VOLUME ["/config"]

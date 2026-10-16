# Obraz serwera USBShare (linux/amd64, linux/arm64, linux/arm/v7)
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/usbshare-server ./cmd/usbshare-server

FROM alpine:3.20
# kmod: kontener może sam załadować usbip-host, jeśli zamontowano /lib/modules hosta
RUN apk add --no-cache kmod
COPY --from=build /out/usbshare-server /usr/local/bin/usbshare-server
VOLUME /data
EXPOSE 7600
ENTRYPOINT ["/usr/local/bin/usbshare-server"]

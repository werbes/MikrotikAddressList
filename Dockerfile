FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build

ENV GOTOOLCHAIN=local \
    GOPROXY=off

WORKDIR /src
COPY go.mod ./
COPY main.go config.go server.go catalog.go cidr.go mikrotik.go updater.go ./
COPY web ./web
RUN apk add --no-cache ca-certificates \
    && CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -buildvcs=false \
        -ldflags="-s -w -buildid=" \
        -o /out/mikrotik-address-list . \
    && mkdir -p /out/data \
    && chown -R 65532:65532 /out

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/mikrotik-address-list /usr/local/bin/mikrotik-address-list
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532
ENV LISTEN_ADDR=:8080
ENV DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080
STOPSIGNAL SIGTERM

ENTRYPOINT ["/usr/local/bin/mikrotik-address-list"]

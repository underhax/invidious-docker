FROM quay.io/invidious/invidious:latest@sha256:3ddcd912e86339b333d19fcca8bc46e59c0ce0e4e367130f11c1dede69463485 AS invidious-donor

FROM quay.io/invidious/invidious-companion:latest@sha256:55f0bc1002c0edbe4e8163d503c7d797ed919e5861e2c0d0f829ae80138237a4 AS companion-donor

FROM golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS go-builder

WORKDIR /build

COPY go.mod ./
COPY init/ ./init/
COPY probe/ ./probe/

RUN --mount=from=invidious-donor,source=/invidious,target=/mnt/invidious \
    --mount=from=companion-donor,source=/,target=/mnt/companion \
    mkdir -p /out/invidious/etc/ssl/certs \
             /out/invidious/invidious \
             /out/invidious/var/run/postgresql \
             /out/invidious/var/run/companion \
             /out/invidious/tmp \
             /out/companion/etc/ssl/certs \
             /out/companion/usr \
             /out/companion/var/tmp/youtubei.js \
             /out/companion/tmp \
             /out/usr/local/bin && \
    chmod 0755 /out/invidious/etc /out/invidious/etc/ssl /out/invidious/etc/ssl/certs \
               /out/invidious/invidious /out/invidious/var /out/invidious/var/run \
               /out/companion/etc /out/companion/etc/ssl /out/companion/etc/ssl/certs \
               /out/companion/usr /out/usr /out/usr/local /out/usr/local/bin && \
    chmod 0700 /out/invidious/var/run/postgresql \
               /out/invidious/var/run/companion \
               /out/companion/var/tmp/youtubei.js \
               /out/companion/tmp && \
    chmod 1777 /out/invidious/tmp && \
    cp /etc/ssl/certs/ca-certificates.crt /out/invidious/etc/ssl/certs/ca-certificates.crt && \
    cp /etc/ssl/certs/ca-certificates.crt /out/companion/etc/ssl/certs/ca-certificates.crt && \
    chmod 0444 /out/invidious/etc/ssl/certs/ca-certificates.crt \
               /out/companion/etc/ssl/certs/ca-certificates.crt && \
    cp -R /mnt/invidious/assets \
          /mnt/invidious/locales \
          /mnt/invidious/config \
          /mnt/invidious/invidious \
          /out/invidious/invidious/ && \
    chmod 0555 /out/invidious/invidious/invidious && \
    cp /mnt/companion/etc/nsswitch.conf /out/companion/etc/nsswitch.conf && \
    chmod 0444 /out/companion/etc/nsswitch.conf && \
    cp -R /mnt/companion/etc/ld.so.conf.d /out/companion/etc/ld.so.conf.d && \
    for libdir in lib lib64; do \
      if [ -d "/mnt/companion/usr/${libdir}" ]; then \
        cp -RL "/mnt/companion/usr/${libdir}" "/out/companion/usr/${libdir}" && \
        ln -s "usr/${libdir}" "/out/companion/${libdir}"; \
      fi; \
    done && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/usr/local/bin/init ./init && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/usr/local/bin/probe ./probe

FROM scratch AS companion

COPY --from=go-builder /out/companion/ /
COPY --from=companion-donor --chmod=0555 /app/invidious_companion /app/invidious_companion
COPY --from=companion-donor --chmod=0444 /app/config/config.example.toml /app/config/config.example.toml
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/init /usr/local/bin/init
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/probe /usr/local/bin/probe

WORKDIR /app

ENTRYPOINT ["/usr/local/bin/init", "/app/invidious_companion"]

FROM scratch AS invidious

COPY --from=go-builder /out/invidious/ /
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/init /usr/local/bin/init
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/probe /usr/local/bin/probe

WORKDIR /invidious

ENTRYPOINT ["/usr/local/bin/init"]

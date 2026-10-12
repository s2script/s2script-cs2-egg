# SPDX-License-Identifier: GPL-3.0-or-later
# linux/amd64 only. Both targets build from source; no unpublished image dependency.
FROM --platform=linux/amd64 golang:1.27.2-alpine@sha256:3b15ba438c60ab8fabe5fee3b11025c0049e358b31feff384648f11fc8e335f8 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/s2egg ./cmd/s2egg

FROM --platform=linux/amd64 registry.gitlab.steamos.cloud/steamrt/sniper/platform@sha256:8cd1bdfc418afd8b019a69d6f5a9305865a5f3bb5554102006fdb8f6853d5f67 AS steamrt

# Re-export SteamRT's rootfs to normalize its absolute hardlink targets.
# Approach researched from K4ryuu/CS2-Egg-Go; see NOTICE.
FROM scratch AS base
COPY --from=steamrt / /
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
RUN useradd --uid 1000 --create-home --home-dir /home/container --shell /bin/bash container \
 && mkdir -p /opt/s2egg/steamcmd /usr/local/share/licenses/s2egg \
 && curl --fail --show-error --location --proto '=https' --proto-redir '=https' \
      https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz -o /tmp/steamcmd.tar.gz \
 && printf '%s\n' 'cebf0046bfd08cf45da6bc094ae47aa39ebf4155e5ede41373b579b8f1071e7c  /tmp/steamcmd.tar.gz' | sha256sum --check - \
 && tar --no-same-owner -xzf /tmp/steamcmd.tar.gz -C /opt/s2egg/steamcmd \
 && chmod -R a+rX,go-w /opt/s2egg/steamcmd \
 && rm /tmp/steamcmd.tar.gz \
 && test -x /bin/bash \
 && test -e /lib/ld-linux.so.2
COPY --from=build --chmod=0555 /out/s2egg /usr/local/bin/s2egg
COPY LICENSE NOTICE third_party/Go-LICENSE /usr/local/share/licenses/s2egg/
LABEL org.opencontainers.image.source="https://github.com/s2script/s2script-cs2-egg" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.description="CS2 with Metamod and S2Script; standalone supervised lifecycle"
ENV USER=container HOME=/home/container
WORKDIR /home/container
USER container
STOPSIGNAL SIGTERM
CMD ["/usr/local/bin/s2egg", "run", "--root", "/home/container"]

# Explicit opt-in target. GDB_DEBUG_PORT still defaults to 0 in both images.
FROM base AS debug
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends gdb gdbserver \
 && rm -rf /var/lib/apt/lists/*
USER container

# Keep the default final target free of the additional debugging packages.
FROM base AS runtime

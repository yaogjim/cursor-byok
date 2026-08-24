# cursor-cli runtime: oven/bun + network tools + WeTTY (xterm.js).
# WeTTY listens on 127.0.0.1:7681. Do not publish that port.
# Interactive PTY is bun's /bin/sh; the WeTTY server itself must be root
# because non-root WeTTY always ssh(1) to localhost (dangerous with --network host).
FROM oven/bun:1.3.14-slim

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY=127.0.0.1,localhost,::1,172.16.0.0/12,192.168.0.0/16
ARG WETTY_VERSION=2.5.0
ARG NODE_VERSION=22.17.0

ENV DEBIAN_FRONTEND=noninteractive \
    HOME=/home/bun \
    NO_OPEN_BROWSER=1 \
    PATH=/home/bun/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/bun-node-fallback-bin \
    NODE_ENV=production

# Do not apt-install Debian's `npm` package: it pulls hundreds of node-* debs
# and routinely 502s through the site HTTP proxy.
RUN set -eux; \
    if [ -n "${HTTP_PROXY:-}" ]; then \
      printf 'Acquire::http::Proxy "%s";\nAcquire::https::Proxy "%s";\nAcquire::Retries "8";\n' \
        "${HTTP_PROXY}" "${HTTPS_PROXY:-$HTTP_PROXY}" > /etc/apt/apt.conf.d/99proxy; \
    else \
      printf 'Acquire::Retries "8";\n' > /etc/apt/apt.conf.d/99retries; \
    fi; \
    export http_proxy="${HTTP_PROXY:-}" https_proxy="${HTTPS_PROXY:-${HTTP_PROXY:-}}" \
      HTTP_PROXY="${HTTP_PROXY:-}" HTTPS_PROXY="${HTTPS_PROXY:-${HTTP_PROXY:-}}" \
      no_proxy="${NO_PROXY}" NO_PROXY="${NO_PROXY}"; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      wget \
      git \
      iproute2 \
      iputils-ping \
      netcat-openbsd \
      bind9-dnsutils \
      traceroute \
      openssl \
      procps \
      python3 \
      python3-setuptools \
      make \
      g++ \
      xz-utils \
      util-linux; \
    rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    export http_proxy="${HTTP_PROXY:-}" https_proxy="${HTTPS_PROXY:-${HTTP_PROXY:-}}" \
      HTTP_PROXY="${HTTP_PROXY:-}" HTTPS_PROXY="${HTTPS_PROXY:-${HTTP_PROXY:-}}" \
      no_proxy="${NO_PROXY}" NO_PROXY="${NO_PROXY}"; \
    node_tar="node-v${NODE_VERSION}-linux-x64.tar.xz"; \
    url="https://nodejs.org/dist/v${NODE_VERSION}/${node_tar}"; \
    mirror="https://npmmirror.com/mirrors/node/v${NODE_VERSION}/${node_tar}"; \
    if ! curl -fL --retry 8 --retry-all-errors --retry-delay 3 -o "/tmp/${node_tar}" "$url"; then \
      curl -fL --retry 8 --retry-all-errors --retry-delay 3 -o "/tmp/${node_tar}" "$mirror"; \
    fi; \
    tar -xJf "/tmp/${node_tar}" -C /usr/local --strip-components=1; \
    rm -f "/tmp/${node_tar}"; \
    hash -r; \
    node --version; \
    npm --version; \
    npm config set fetch-retries 8; \
    npm config set fetch-retry-mintimeout 20000; \
    npm config set fetch-retry-maxtimeout 120000; \
    npm install -g --omit=dev --unsafe-perm --foreground-scripts "wetty@${WETTY_VERSION}"; \
    command -v wetty; \
    wetty --help >/dev/null; \
    npm cache clean --force; \
    rm -rf /root/.npm /tmp/npm-*

RUN set -eux; \
    if [ -n "${HTTP_PROXY:-}" ]; then \
      printf 'Acquire::http::Proxy "%s";\nAcquire::https::Proxy "%s";\nAcquire::Retries "8";\n' \
        "${HTTP_PROXY}" "${HTTPS_PROXY:-$HTTP_PROXY}" > /etc/apt/apt.conf.d/99proxy; \
    fi; \
    export http_proxy="${HTTP_PROXY:-}" https_proxy="${HTTPS_PROXY:-${HTTP_PROXY:-}}" \
      HTTP_PROXY="${HTTP_PROXY:-}" HTTPS_PROXY="${HTTPS_PROXY:-${HTTP_PROXY:-}}" \
      no_proxy="${NO_PROXY}" NO_PROXY="${NO_PROXY}"; \
    apt-get update; \
    apt-get purge -y python3 python3-setuptools make g++ xz-utils; \
    apt-get autoremove -y --purge; \
    rm -rf /var/lib/apt/lists/* /root/.npm /tmp/npm-*

COPY cursor-cli-shell.sh /usr/local/bin/cursor-cli-shell
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY patch-wetty.cjs /usr/local/lib/cursor-cli-docker/patch-wetty.cjs
RUN chmod 0755 /usr/local/bin/cursor-cli-shell /usr/local/bin/docker-entrypoint.sh \
    && node /usr/local/lib/cursor-cli-docker/patch-wetty.cjs \
    && mkdir -p /workspace \
    && chown bun:bun /workspace

WORKDIR /workspace
# Documented only. With --network host this does not publish the port.
EXPOSE 7681
USER root
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
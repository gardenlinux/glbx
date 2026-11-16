FROM debian:stable

ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive

RUN set -eu; \
    case "$TARGETARCH" in \
        amd64) set -- qemu-system-x86 ovmf ;; \
        arm64) set -- qemu-system-arm qemu-efi-aarch64 ;; \
        *) echo "Unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        bzip2 \
        ca-certificates \
        git \
        gpgv \
        libcap2-bin \
        qemu-utils \
        sudo \
        uidmap \
        xz-utils \
        zstd \
        "$@"; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

RUN chmod -s /usr/bin/newuidmap \
 && setcap cap_setuid=ep /usr/bin/newuidmap \
 && chmod -s /usr/bin/newgidmap \
 && setcap cap_setgid=ep /usr/bin/newgidmap

RUN groupadd -g 1000 dev \
 && useradd -m -u 1000 -g 1000 -s /bin/bash dev \
 && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev \
 && chmod 0440 /etc/sudoers.d/dev \
 && echo 'dev:100000:65536' >> /etc/subuid \
 && echo 'dev:100000:65536' >> /etc/subgid

USER dev
WORKDIR /home/dev/workspace

ENV PATH=/home/dev/.local/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin

COPY bin-$TARGETARCH/glbx bin-$TARGETARCH/exec_env_stub /home/dev/.local/bin/

RUN git config --global --add safe.directory /home/dev/workspace

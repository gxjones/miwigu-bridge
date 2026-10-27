#!/bin/sh
# Build guacd with RDP support and install it under /usr/local.
# FreeRDP comes from the distro: FreeRDP 2 on Debian, FreeRDP 3 on Amazon Linux 2023.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
VERSION=1.6.0
PREFIX=/usr/local
SRC=/tmp/guacamole-server-build
URL="https://archive.apache.org/dist/guacamole/${VERSION}/source/guacamole-server-${VERSION}.tar.gz"
# GUACAMOLE-2328, fixed for 1.6.1: 1.6.0 calls gdi_init() in PreConnect, which
# segfaults on every RDP connection with FreeRDP 3.31 and later (AL2023 ships 3.32).
PATCH="$HERE/patches/guacamole-2328-${VERSION}.patch"

install_deps() {
    if command -v dnf >/dev/null 2>&1; then
        dnf install -y \
            gcc make autoconf automake libtool pkgconfig patch \
            tar gzip ca-certificates \
            cairo cairo-devel \
            libjpeg-turbo libjpeg-turbo-devel \
            libpng libpng-devel \
            libuuid libuuid-devel \
            openssl openssl-devel \
            freerdp freerdp-libs freerdp-devel \
            libwinpr libwinpr-devel
        if ! command -v curl >/dev/null 2>&1; then
            dnf install -y curl-minimal || dnf install -y curl
        fi
    elif command -v apt-get >/dev/null 2>&1; then
        apt-get update
        apt-get install -y --no-install-recommends \
            build-essential autoconf automake libtool pkg-config patch \
            ca-certificates curl tar gzip \
            libcairo2 libcairo2-dev \
            libjpeg62-turbo libjpeg62-turbo-dev \
            libpng16-16 libpng-dev \
            libossp-uuid16 libossp-uuid-dev \
            libssl3 libssl-dev \
            freerdp2-dev
    else
        echo "install-guacd.sh needs dnf or apt-get" >&2
        exit 1
    fi
}

drop_build_deps() {
    if command -v dnf >/dev/null 2>&1; then
        dnf remove -y \
            gcc make autoconf automake libtool pkgconfig patch \
            cairo-devel libjpeg-turbo-devel libpng-devel libuuid-devel \
            openssl-devel freerdp-devel libwinpr-devel \
            || true
        dnf install -y \
            cairo libjpeg-turbo libpng libuuid openssl \
            freerdp freerdp-libs libwinpr
        dnf clean all
    elif command -v apt-get >/dev/null 2>&1; then
        apt-get purge -y \
            build-essential autoconf automake libtool pkg-config patch \
            libcairo2-dev libjpeg62-turbo-dev libpng-dev \
            libossp-uuid-dev libssl-dev freerdp2-dev \
            || true
        apt-get install -y --no-install-recommends \
            libcairo2 libjpeg62-turbo libpng16-16 libossp-uuid16 libssl3 \
            libfreerdp2-2 libfreerdp-client2-2 libwinpr2-2
        rm -rf /var/lib/apt/lists/*
    fi
}

install_deps

rm -rf "$SRC"
mkdir -p "$SRC"
cd "$SRC"
curl -fsSL -o "guacamole-server-${VERSION}.tar.gz" "$URL"
curl -fsSL -o "guacamole-server-${VERSION}.tar.gz.sha256" "${URL}.sha256"
sha256sum -c "guacamole-server-${VERSION}.tar.gz.sha256"
tar -xzf "guacamole-server-${VERSION}.tar.gz"
cd "guacamole-server-${VERSION}"
patch -p1 < "$PATCH"

# FreeRDP 3 headers warn, and configure tests those headers with -Werror.
export CPPFLAGS="-Wno-error=deprecated-declarations ${CPPFLAGS:-}"
export CFLAGS="-Wno-error=deprecated-declarations ${CFLAGS:-}"
./configure --prefix="$PREFIX" --with-init-dir=/tmp/guac-extra --with-systemd-dir=/tmp/guac-extra \
    > /tmp/guac-configure.txt
if ! grep -E 'RDP [.]+ yes' /tmp/guac-configure.txt >/dev/null; then
    cat /tmp/guac-configure.txt >&2
    echo "guacamole-server was configured without RDP" >&2
    exit 1
fi
make -j"$(nproc 2>/dev/null || echo 2)"
make install
echo "$PREFIX/lib" > /etc/ld.so.conf.d/guacamole.conf
ldconfig

if ldd "$PREFIX/sbin/guacd" | grep -q "not found"; then
    ldd "$PREFIX/sbin/guacd" >&2
    echo "guacd is missing shared libraries" >&2
    exit 1
fi

cd /
rm -rf "$SRC" /tmp/guac-extra
drop_build_deps
ldconfig
if ldd "$PREFIX/sbin/guacd" | grep -q "not found"; then
    ldd "$PREFIX/sbin/guacd" >&2
    echo "guacd is missing shared libraries after removing build packages" >&2
    exit 1
fi
echo "guacd installed at $PREFIX/sbin/guacd"

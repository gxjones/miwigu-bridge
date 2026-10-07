#!/bin/sh
# Cross-build the two native libraries for a Graviton (aarch64) Linux guest.
set -eu
. "$HOME/.cargo/env"
cd "$(dirname "$0")"

target=aarch64-unknown-linux-gnu
out=dist/$target
linker=aarch64-linux-gnu-gcc

command -v "$linker" >/dev/null
rustup target add "$target"

mkdir -p "$out"
make -C lwip_port OUT="../$out/liblwip_port.so" CC="$linker"

(
    cd tunn_ffi
    export CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER="$linker"
    export CC_aarch64_unknown_linux_gnu="$linker"
    export AR_aarch64_unknown_linux_gnu=aarch64-linux-gnu-ar
    cargo build --release --target "$target"
)
cp "tunn_ffi/target/$target/release/libtunn_ffi.so" "$out/libtunn_ffi.so"

echo "built:"
file "$out/liblwip_port.so" "$out/libtunn_ffi.so"

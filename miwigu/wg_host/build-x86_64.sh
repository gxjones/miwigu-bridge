#!/bin/sh
# Build the two native libraries for this server (x86-64 Linux).
set -eu
. "$HOME/.cargo/env"
cd "$(dirname "$0")"

target=x86_64-unknown-linux-gnu
out=dist/$target

mkdir -p "$out"
make -C lwip_port OUT="../$out/liblwip_port.so" CC=gcc
cp "$out/liblwip_port.so" lwip_port/liblwip_port.so

(
    cd tunn_ffi
    cargo build --release --target "$target"
)
cp "tunn_ffi/target/$target/release/libtunn_ffi.so" "$out/libtunn_ffi.so"
mkdir -p tunn_ffi/target/release
cp "$out/libtunn_ffi.so" tunn_ffi/target/release/libtunn_ffi.so

echo "built:"
file "$out/liblwip_port.so" "$out/libtunn_ffi.so"

#!/bin/sh
# Сборка bt-proxy: статический ARMv7 musl (работает и на 64-битных ядрах колонок).
# Нужны rustup и встроенный rust-lld; библиотеки прошивки не требуются.
set -eu
cd "$(dirname "$0")"
rustup target add armv7-unknown-linux-musleabihf
target=armv7-unknown-linux-musleabihf
host=$(rustc -vV | sed -n 's/^host: //p')
linker=$(rustc --print sysroot)/lib/rustlib/$host/bin/rust-lld
test -x "$linker"
# пути исходников попадают в сообщения паник — не публиковать домашний каталог сборщика
RUSTFLAGS="--remap-path-prefix=$HOME=~ --remap-path-prefix=$PWD=bt-proxy -C linker=$linker -C linker-flavor=gnu-lld -C link-self-contained=yes -C target-feature=+crt-static" \
  cargo build --locked --release --target "$target"
mkdir -p ../dist
cp "target/$target/release/bt-proxy" ../dist/bt-proxy

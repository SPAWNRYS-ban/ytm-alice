#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
container=''
cleanup() { if [ -n "$container" ]; then docker rm "$container" >/dev/null; fi; }
trap cleanup EXIT
docker build -t ytm-alice-native-builder:0.7 "$root/native"
container=$(docker create ytm-alice-native-builder:0.7)
mkdir -p "$root/dist"
docker cp "$container:/native-player" "$root/dist/ytm-alice-player"
chmod 755 "$root/dist/ytm-alice-player"
printf 'Built %s/dist/ytm-alice-player without device access.\n' "$root"

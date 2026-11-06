#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
container=''
cleanup() { if [ -n "$container" ]; then docker rm "$container" >/dev/null; fi; }
trap cleanup EXIT
python3 "$root/native/fetch-quickjs.py"
docker build -t ytm-alice-native-builder:0.7 "$root/native"
container=$(docker create ytm-alice-native-builder:0.7)
mkdir -p "$root/dist"
docker cp "$container:/native-player" "$root/dist/ytm-alice-player-armhf"
docker cp "$container:/native-resolver" "$root/dist/ytm-alice-resolver"
docker cp "$container:/native-player-arm64" "$root/dist/ytm-alice-player-arm64"
cp "$root/native/player-launcher" "$root/dist/ytm-alice-player"
chmod 755 "$root/dist/ytm-alice-player-armhf" "$root/dist/ytm-alice-player-arm64"
chmod 755 "$root/dist/ytm-alice-player" "$root/dist/ytm-alice-resolver"
printf 'Built %s/dist/ytm-alice-player without device access.\n' "$root"

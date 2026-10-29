#!/bin/sh
set -eu
: "${LUNA_API_URL:?ytm-alice must be started by Luna}"
: "${LUNA_API_TOKEN:?Luna runtime token missing}"
: "${YAM_ROOT:?YAM root missing}"
export YTM_ALICE_DATA="$YAM_ROOT/var/local.ytm-alice"
exec "$YAM_ROOT/bin/ytm-alice" serve

#!/bin/sh
# Build <jool image> (Jool's NAT64 modules) for <kernel image> (pulled if not local): jool/Dockerfile.
#
#   jool/jool.sh <kernel image> <jool image>
set -eu
KERNEL=$1 OUT=$2
docker image inspect "$KERNEL" >/dev/null 2>&1 || docker pull -q "$KERNEL" >/dev/null
BUILD_IMAGE=$(docker image inspect --format '{{index .Config.Labels "org.mobyproject.linuxkit.kernel.buildimage"}}' "$KERNEL")
[ -n "$BUILD_IMAGE" ] || { echo "$KERNEL: no builder image label" >&2; exit 1; }
docker build -t "$OUT" --build-arg "KERNEL_IMAGE=$KERNEL" --build-arg "BUILD_IMAGE=$BUILD_IMAGE" \
	-f "$(dirname "$0")/Dockerfile" "$(dirname "$0")"

#!/bin/sh
# Build <jool image> (Jool's NAT64 modules) for <kernel image>, both local: kernel/jool.Dockerfile.
#
#   kernel/jool.sh <kernel image> <jool image>
set -eu
KERNEL=$1 OUT=$2
BUILD_IMAGE=$(docker image inspect --format '{{index .Config.Labels "org.mobyproject.linuxkit.kernel.buildimage"}}' "$KERNEL")
[ -n "$BUILD_IMAGE" ] || { echo "$KERNEL: no builder image label" >&2; exit 1; }
docker build -t "$OUT" --build-arg "KERNEL_IMAGE=$KERNEL" --build-arg "BUILD_IMAGE=$BUILD_IMAGE" \
	-f "$(dirname "$0")/jool.Dockerfile" "$(dirname "$0")"

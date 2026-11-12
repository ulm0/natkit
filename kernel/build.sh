#!/bin/sh
# Build linuxkit/kernel:<version>-natkit for one architecture from LinuxKit's kernel packaging + kernel/natkit.config
# and kernel/natkit-<arch>.config (a complete option list over allnoconfig; LinuxKit's own config is not used).
# Natively: the arch is the docker daemon's.
#
#   kernel/build.sh [KERNEL_VERSION]        e.g. kernel/build.sh 6.18.62
#
# A version whose series LinuxKit doesn't package yet borrows the newest series' packaging (Dockerfile,
# build-args); olddefconfig gives new options their defaults. Review kernel/config-<arch> after that.
# Needs docker; ~15 min on 4 CPUs.
set -eu

KERNEL_VERSION=${1:-6.18.62}
LINUXKIT_REF=${LINUXKIT_REF:-f3906d1f6650f79d74adf3cba42b04ca9e21ea7d} # linuxkit/linuxkit master, 2026-10-15
LINUXKIT=$(cd "$(dirname "${LINUXKIT:-bin/linuxkit}")" && pwd)/$(basename "${LINUXKIT:-bin/linuxkit}")
SERIES=$(echo "$KERNEL_VERSION" | cut -d. -f1-2).x
DIR=$(cd "$(dirname "$0")" && pwd)
SRC=$DIR/.build/linuxkit
case $(docker version -f '{{.Server.Arch}}') in
arm64 | aarch64) PLATFORM=linux/arm64 KARCH=arm64 UNAME=aarch64 ;;
amd64 | x86_64) PLATFORM=linux/amd64 KARCH=x86_64 UNAME=x86_64 ;;
*) echo "unsupported docker server arch" >&2 && exit 1 ;;
esac

# 1. LinuxKit's kernel packaging at a pinned commit
if [ ! -d "$SRC/.git" ]; then
	git clone -q https://github.com/linuxkit/linuxkit.git "$SRC"
fi
git -C "$SRC" fetch -q --depth 1 origin "$LINUXKIT_REF"
git -C "$SRC" checkout -q --force "$LINUXKIT_REF"
K=$SRC/kernel

# 2. series dir; for a series LinuxKit doesn't have, copy the newest one
if [ ! -d "$K/$SERIES" ]; then
	BASE=$(cd "$K" && printf '%s\n' [0-9]*.x | grep -v -- -rt | sort -V | tail -n 1)
	echo "LinuxKit has no $SERIES; starting from $BASE"
	cp -R "$K/$BASE" "$K/$SERIES"
	rm -rf "$K/$SERIES/patches" # patches are series-specific
fi
sed -i.bak -e "s/^KERNEL_VERSION=.*/KERNEL_VERSION=$KERNEL_VERSION/" -e "s/^KERNEL_SERIES=.*/KERNEL_SERIES=$SERIES/" "$K/$SERIES/build-args"
rm -f "$K/$SERIES/build-args.bak"

# 3. allnoconfig + natkit.config + natkit-<arch>.config, normalized with olddefconfig (LinuxKit's build rejects a
#    config that isn't), in LinuxKit's own builder image: the config records the compiler version
BUILD_IMAGE=$(sed -n 's/^BUILD_IMAGE=//p' "$K/$SERIES/build-args")
docker run --rm --platform "$PLATFORM" -v "$DIR:/k" "$BUILD_IMAGE" sh -euc "
	apk add -q bash build-base bc bison flex perl linux-headers openssl-dev elfutils-dev curl xz >/dev/null
	cd /tmp && curl -fsSL https://cdn.kernel.org/pub/linux/kernel/v${KERNEL_VERSION%%.*}.x/linux-$KERNEL_VERSION.tar.xz | tar xJ
	cd linux-$KERNEL_VERSION && make ARCH=$KARCH allnoconfig >/dev/null
	cat /k/natkit.config /k/natkit-$UNAME.config | grep '^CONFIG_' >/tmp/want
	while IFS== read -r o v; do scripts/config --set-val \"\${o#CONFIG_}\" \"\$v\"; done </tmp/want
	# plain make (the native ARCH: x86, not x86_64): LinuxKit's build diffs the config's header line too
	make olddefconfig >/dev/null
	# a select/depends chain that isn't met silently drops an option: fail instead
	# (=n also holds when the symbol is absent: its menu is off)
	while read -r l; do
		case \"\$l\" in
		*=n) ! grep -q \"^\${l%=n}=\" .config ;;
		*) grep -qx \"\$l\" .config ;;
		esac || { echo \"not applied: \$l\"; exit 1; }
	done </tmp/want
	cp .config /k/config-$UNAME"
cp "$DIR/config-$UNAME" "$K/$SERIES/config-$UNAME"

# 4. build with LinuxKit's own kernel packaging (same as kernel/Makefile buildplainkernel-%)
(cd "$K" && "$LINUXKIT" pkg build . --docker --force --force-dirty --platforms "$PLATFORM" \
	--build-yml ./common/build-kernel.yml --hash natkit --tag "$KERNEL_VERSION-{{.Hash}}" \
	--build-arg-file "$SERIES/build-args" --progress plain)
echo "built linuxkit/kernel:$KERNEL_VERSION-natkit ($PLATFORM)"

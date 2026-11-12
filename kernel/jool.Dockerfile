# Jool's NAT64 kernel modules for one natkit kernel, as a LinuxKit init image: /lib/modules/<release>/extra/*.ko,
# merged into the root filesystem. Built against the kernel image's kernel-dev.tar (headers, .config,
# Module.symvers, host tools) in the kernel's own builder image (same compiler). kernel/jool.sh runs it.
ARG KERNEL_IMAGE
ARG BUILD_IMAGE
FROM ${KERNEL_IMAGE} AS kernel

FROM ${BUILD_IMAGE} AS build
ARG JOOL_VERSION=4.1.15
ARG JOOL_SHA256=51604bcf6a9f7fbb9080876102d05da01dc91d8c73cd5828fd73d5895643f8cd
RUN apk add -q build-base bc bison flex perl linux-headers openssl-dev elfutils-dev curl
COPY --from=kernel /kernel-dev.tar /
RUN tar xf /kernel-dev.tar -C / && \
	curl -fsSLo /jool.tgz https://github.com/NICMx/Jool/releases/download/v$JOOL_VERSION/jool-$JOOL_VERSION.tar.gz && \
	echo "$JOOL_SHA256  /jool.tgz" | sha256sum -c - && tar xzf /jool.tgz -C / && \
	set -- /usr/src/linux-headers-* && rel=${1#/usr/src/linux-headers-} && \
	make -C /jool-$JOOL_VERSION/src/mod/common KERNEL_DIR="$1" && \
	make -C /jool-$JOOL_VERSION/src/mod/nat64 KERNEL_DIR="$1" && \
	mkdir -p /out/lib/modules/$rel/extra && \
	cp /jool-$JOOL_VERSION/src/mod/common/jool_common.ko /jool-$JOOL_VERSION/src/mod/nat64/jool.ko /out/lib/modules/$rel/extra/

FROM scratch
COPY --from=build /out/ /

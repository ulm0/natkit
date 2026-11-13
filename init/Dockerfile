# Jool's userspace tool, the same version as the kernel modules (kernel/jool.sh): Alpine's jool-tools lags
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS jool
ARG JOOL_VERSION=4.1.15
ARG JOOL_SHA256=51604bcf6a9f7fbb9080876102d05da01dc91d8c73cd5828fd73d5895643f8cd
RUN apk add --no-cache build-base linux-headers argp-standalone bsd-compat-headers libnl3-dev pkgconf curl
RUN curl -fsSLo /jool.tgz https://github.com/NICMx/Jool/releases/download/v$JOOL_VERSION/jool-$JOOL_VERSION.tar.gz && \
	echo "$JOOL_SHA256  /jool.tgz" | sha256sum -c - && tar xzf /jool.tgz -C / && cd /jool-$JOOL_VERSION && \
	./configure --prefix=/usr --disable-shared --with-xtables=no >/dev/null && \
	make -s && make -s install DESTDIR=/out

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# curl --aws-sigv4 signs the EC2 and SSM calls: no aws-cli (~100 MB) in an image that lives in RAM
RUN apk add --no-cache curl yq-go nftables iproute2 libnl3
COPY --from=jool /out/usr/bin/jool /usr/bin/jool
COPY natkit-init natkit-cwagent /usr/bin/
COPY sysctl.conf /etc/natkit/sysctl.conf
LABEL org.opencontainers.image.source=https://github.com/ulm0/natkit
ENTRYPOINT ["/usr/bin/natkit-init"]

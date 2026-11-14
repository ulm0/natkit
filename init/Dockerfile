# natkit (natkit-init, natkit-metrics, natkit-cwagent): one static Go binary, the AWS SDK for Go v2 inside it
FROM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS natkit
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /natkit .

# Jool's userspace tool, the same version as the kernel modules (jool/jool.sh): Alpine's jool-tools lags
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS jool
ARG JOOL_VERSION=4.1.15
ARG JOOL_SHA256=51604bcf6a9f7fbb9080876102d05da01dc91d8c73cd5828fd73d5895643f8cd
RUN apk add --no-cache build-base linux-headers argp-standalone bsd-compat-headers libnl3-dev pkgconf curl
RUN curl -fsSLo /jool.tgz https://github.com/NICMx/Jool/releases/download/v$JOOL_VERSION/jool-$JOOL_VERSION.tar.gz && \
	echo "$JOOL_SHA256  /jool.tgz" | sha256sum -c - && tar xzf /jool.tgz -C / && cd /jool-$JOOL_VERSION && \
	./configure --prefix=/usr --disable-shared --with-xtables=no >/dev/null && \
	make -s && make -s install DESTDIR=/out

# What natkit runs besides itself: nft (the ruleset), ethtool (the ENA allowance counters) and Jool's tool, which
# needs libnl3. No shell tooling: the image lives in RAM.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache nftables libnl3 ethtool
COPY --from=jool /out/usr/bin/jool /usr/bin/jool
COPY --from=natkit /natkit /usr/bin/natkit
RUN ln -s natkit /usr/bin/natkit-init && ln -s natkit /usr/bin/natkit-metrics && ln -s natkit /usr/bin/natkit-cwagent
COPY sysctl.conf /etc/natkit/sysctl.conf
LABEL org.opencontainers.image.source=https://github.com/ulm0/natkit
ENTRYPOINT ["/usr/bin/natkit-init"]

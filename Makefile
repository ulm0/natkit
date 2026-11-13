export DOCKER_HOST ?= $(shell docker context inspect --format "{{.Endpoints.docker.Host}}")
# linuxkit master: no release since v1.8.2 (2025-09). Built from source; the ref is in the file name so
# bumping it rebuilds.
LINUXKIT_REF := 0e34a007431a3a3c19a03fc6bca00fba51774706 # 2026-10-27
LK := bin/linuxkit-$(shell echo $(LINUXKIT_REF) | cut -c1-12)
GOLANG := golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59
ALPINE := alpine:3.24@sha256:3f4d6b32e26703c6082839234f43769d5046b3ac7c910033911d1e71d8cd630a
REGISTRY := ghcr.io/ulm0/natkit
# images natkit.yml uses; local builds tag these names, otherwise linuxkit pulls them from GHCR (CI pushes them)
INIT_IMAGE := $(REGISTRY)/init:latest
# the kernel natkit.yml pins (github.com/ulm0/lk-kernel), and Jool's modules built for it under its version
KERNEL_IMAGE := $(shell sed -n 's|^  image: \(ghcr.io/ulm0/lk-kernel:.*\)$$|\1|p' natkit.yml)
KERNEL_VERSION := $(shell echo '$(KERNEL_IMAGE)' | sed 's|.*:\([0-9.]*\)@.*|\1|')
JOOL_IMAGE := $(REGISTRY)/jool:$(KERNEL_VERSION)
# the docker daemon's architecture: everything builds natively (arm64 or amd64)
ARCH ?= $(shell docker version -f '{{.Server.Arch}}' | sed 's/aarch64/arm64/;s/x86_64/amd64/')
# the arm64 UKI needs an uncompressed (PE) kernel; x86's bzImage already is one
DECOMPRESS := $(if $(filter arm64,$(ARCH)),--decompress-kernel)
IMG := out/natkit-$(ARCH)-efi.img

all: $(IMG)

$(LK):
	mkdir -p bin
	curl -fsSL https://codeload.github.com/linuxkit/linuxkit/tar.gz/$(LINUXKIT_REF) | docker run --rm -i \
		-e CGO_ENABLED=0 -e GOOS=$$(uname -s | tr A-Z a-z) -e GOARCH=$$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/') \
		$(GOLANG) sh -c 'mkdir /s && cd /s && tar xz --strip-components=1 && cd src/cmd/linuxkit && \
		go build -trimpath -ldflags "-s -w -X github.com/linuxkit/linuxkit/src/cmd/linuxkit/version.GitCommit=$(LINUXKIT_REF) \
		-X github.com/linuxkit/linuxkit/src/cmd/linuxkit/version.Version=master" -o /lk . >&2 && cat /lk' >$@.tmp
	chmod +x $@.tmp && mv $@.tmp $@

init-image: $(wildcard init/*)
	docker build -t $(INIT_IMAGE) init
	touch $@

# raw-efi: GPT disk with an ESP holding systemd-boot and a UKI (kernel + initramfs root + cmdline).
# scripts/esp.sh then reformats the ESP, which EC2's UEFI can't read as linuxkit makes it.
$(IMG): natkit.yml init-image scripts/esp.sh | $(LK)
	mkdir -p out && $(LK) build --docker $(DECOMPRESS) --format raw-efi --arch $(ARCH) --dir out --name natkit-$(ARCH) natkit.yml
	docker run --rm -i -v $(CURDIR)/scripts/esp.sh:/esp.sh:ro $(ALPINE) sh -c \
		'apk add -q dosfstools mtools sfdisk >&2 && cat >/d.img && sh /esp.sh /d.img && cat /d.img' <$@ >$@.tmp
	mv $@.tmp $@

# Jool's modules for natkit.yml's kernel, for the docker daemon's architecture (otherwise linuxkit pulls them)
.PHONY: jool
jool:
	jool/jool.sh $(KERNEL_IMAGE) $(JOOL_IMAGE)

# AMI from $(IMG): needs AWS credentials, AWS_REGION, NATKIT_BUCKET (scripts/ami.sh)
AMI_NAME ?= natkit-$(shell git describe --tags --always --dirty)-$(ARCH)
.PHONY: ami test clean
ami: $(IMG)
	scripts/ami.sh $< $(ARCH) $(AMI_NAME) natkit.io/release=$(AMI_NAME)

test:
	test/init.sh

clean:
	rm -rf out init-image

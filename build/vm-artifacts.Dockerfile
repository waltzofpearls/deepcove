# Builds the VM artifacts a release carries, one stage per artifact, so the same steps run on a laptop,
# a droplet and a runner. Every stage ends in scratch, so `--output type=local` writes the tarballs and
# nothing else.
#
#   docker buildx build -f build/vm-artifacts.Dockerfile --target passt \
#     --platform linux/arm64 --output type=local,dest=dist/release .
#
# TARGETARCH is amd64 or arm64, the same names the artifacts use. Nothing is cross compiled, so pick a
# platform your machine can run: native, or emulated if you are willing to wait.

FROM debian:trixie-slim AS tools
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl jq tar xz-utils zstd \
	&& rm -rf /var/lib/apt/lists/*
COPY build/vm-artifacts.sh /usr/local/bin/vm-artifacts
COPY internal/vmruntime/versions.json /versions.json
ENV VM_VERSIONS=/versions.json OUT_DIR=/out

FROM tools AS build-guest-kernel
ARG TARGETARCH
RUN vm-artifacts "$TARGETARCH" guest-kernel

FROM scratch AS guest-kernel
COPY --from=build-guest-kernel /out/ /

FROM tools AS build-cloud-hypervisor
ARG TARGETARCH
RUN vm-artifacts "$TARGETARCH" cloud-hypervisor

FROM scratch AS cloud-hypervisor
COPY --from=build-cloud-hypervisor /out/ /

FROM tools AS build-passt
RUN apt-get update \
	&& apt-get install -y --no-install-recommends build-essential \
	&& rm -rf /var/lib/apt/lists/*
ARG TARGETARCH
RUN vm-artifacts "$TARGETARCH" passt

FROM scratch AS passt
COPY --from=build-passt /out/ /

FROM rust:1-slim AS build-virtiofsd
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl jq libcap-ng-dev libseccomp-dev \
		pkg-config \
	&& rm -rf /var/lib/apt/lists/*
COPY build/vm-artifacts.sh /usr/local/bin/vm-artifacts
COPY internal/vmruntime/versions.json /versions.json
ENV VM_VERSIONS=/versions.json OUT_DIR=/out
ARG TARGETARCH
RUN vm-artifacts "$TARGETARCH" virtiofsd

FROM scratch AS virtiofsd
COPY --from=build-virtiofsd /out/ /

FROM scratch AS all
COPY --from=build-guest-kernel /out/ /
COPY --from=build-cloud-hypervisor /out/ /
COPY --from=build-virtiofsd /out/ /
COPY --from=build-passt /out/ /

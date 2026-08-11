# A guest kernel, and nothing else.
#
# Not a runnable image: it exists so the kernel can be distributed and cached the same way session
# images already are, which means deepcove pulls it with the registry client it already has instead of
# growing its own download, checksum and cache code.
#
# The build context holds one kernel per architecture, extracted from a Kata Containers release by
# .github/workflows/deepcove-kernel.yml. TARGETARCH picks the right one, so a single buildx invocation
# publishes one multi-arch index and a client pulls only its own platform.
FROM scratch

ARG TARGETARCH
COPY kernels/${TARGETARCH}/vmlinux /vmlinux

# Provenance, so the binary can be traced back to the upstream release it came from and verified against
# it. Set from the workflow.
ARG KATA_VERSION
ARG KERNEL_VERSION
ARG KERNEL_SHA256
LABEL org.opencontainers.image.source="https://github.com/kata-containers/kata-containers/releases/tag/${KATA_VERSION}"
LABEL org.opencontainers.image.description="Linux guest kernel for deepcove VM sessions, extracted from Kata Containers ${KATA_VERSION}"
LABEL dev.deepcove.kata-version="${KATA_VERSION}"
LABEL dev.deepcove.kernel-version="${KERNEL_VERSION}"
LABEL dev.deepcove.kernel-sha256="${KERNEL_SHA256}"

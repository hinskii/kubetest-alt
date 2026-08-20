# kubetest-alt platform image — kubepug (Kubernetes API deprecation
# scanner) runner.
#
# Rationale (mini-15B closing): the session-15B template originally
# pointed at kubeshop/testkube-kubepug-executor:2.1.123 — that's a
# testkube-vendored image with their own wrapper. Coupling the catalog
# to another test-execution platform is a smell. kubepug is a single
# Go binary published as a release tarball on GitHub, so bundling it
# ourselves is trivial:
#   * Alpine base (kubepug is a static Go binary; no libc quirks).
#   * Pin v1.7.1 (last release at commit time).
#   * Support arm64 + amd64 via TARGETARCH.
#
# Smoke verified: pass=0 (current API) / fail=1 (policy/v1beta1 PDB
# input) on kubetest-alt-kubepug:1.7.1 (arm64 native).

FROM alpine:3.20

ARG TARGETARCH
ARG KUBEPUG_VERSION=1.7.1

RUN set -eux; \
    apk add --no-cache ca-certificates; \
    wget -q -O /tmp/kubepug.tgz \
        "https://github.com/kubepug/kubepug/releases/download/v${KUBEPUG_VERSION}/kubepug_linux_${TARGETARCH}.tar.gz"; \
    tar -xzf /tmp/kubepug.tgz -C /usr/local/bin kubepug; \
    chmod +x /usr/local/bin/kubepug; \
    rm /tmp/kubepug.tgz

# No ENTRYPOINT — the operator's /entry wrapper runs the caller-
# supplied command. Consumers invoke `kubepug` from the catalog
# template.
CMD ["kubepug", "--help"]

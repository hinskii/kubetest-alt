# Init-container image for the content fetcher (step 06).
#
# Two runtime dependencies beyond /entry: `git` for git clones and
# `openssh-client` for SSH auth. Everything else the wrapper needs (curl for
# tarballs, tar/gzip for unpack) is either in Alpine's busybox or the Go
# stdlib (net/http + archive/tar + compress/gzip — the fetcher unpacks in-process).

# ---- build stage --------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY api/ ./api/
COPY pkg/ ./pkg/
COPY internal/ ./internal/
COPY cmd/entry/ ./cmd/entry/

# Same CGO_ENABLED=0 story as executors/k6/Dockerfile — Alpine is musl.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/entry \
    ./cmd/entry

# ---- runtime stage ------------------------------------------------------
# Pinned Alpine version so upgrades are deliberate (git CVEs matter here).
FROM alpine:3.22.1

# git for cloning, openssh-client for SSH auth, ca-certificates so HTTPS
# origins verify. --no-cache keeps the layer lean; no need for apk state.
# Version reproducibility comes from the pinned Alpine tag (3.22.1) — apk's
# repo index for that release is fixed, so `git` alone resolves to a stable
# version. Explicit version pins here would need `apk info -e` verification
# against each new Alpine release.
RUN apk add --no-cache \
    git \
    openssh-client \
    ca-certificates

COPY --from=build /out/entry /entry

# ENTRYPOINT hardcodes the fetch subcommand — this image only ever runs as
# an init container, no reason to expose the runner mode.
ENTRYPOINT ["/entry", "fetch"]

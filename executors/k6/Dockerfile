# Multi-stage build: statically-linked /entry over grafana/k6.
#
# Base image note (CLAUDE.md §11): grafana/k6 ships on Alpine (musl libc).
# CGO-linked Go binaries built against glibc fail to load in that runtime,
# so /entry is built with CGO_ENABLED=0. See executors/k6/Dockerfile change
# in step 05 acceptance if this image ever fails with "not found" on /entry.

# ---- build stage --------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /workspace

# Cache go module downloads across builds.
COPY go.mod go.sum ./
RUN go mod download

# Copy only the sources /entry actually needs — smaller build context, faster
# builds, and no risk of leaking test binaries.
COPY api/ ./api/
COPY pkg/ ./pkg/
COPY cmd/entry/ ./cmd/entry/

# CGO disabled — grafana/k6 uses musl libc; a dynamically-linked binary
# would fail to load libc.so.6 on that runtime.
# -trimpath keeps build paths out of the binary (reproducible builds).
# -ldflags "-s -w" strips the DWARF symbol table (~2MB smaller image).
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/entry \
    ./cmd/entry

# ---- runtime stage ------------------------------------------------------
# Version pin matches compiler.DefaultExecutorImages["k6"] — bump in tandem.
FROM grafana/k6:2.2.0

# Copy the wrapper. grafana/k6's ENTRYPOINT is k6 itself; we override.
COPY --from=build /out/entry /entry

# Default entrypoint so `docker run <image>` invokes the wrapper (matches
# how the operator sets container.command). The wrapper reads request.json
# from /etc/kubetest/ so both compiler and Dockerfile agree via the constant
# executor.RequestPath.
ENTRYPOINT ["/entry"]

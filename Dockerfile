# Multi-purpose Go binary Dockerfile — one file used to build both the
# operator and the apiserver. `--build-arg TARGET_BIN=cmd/operator`
# selects which entrypoint to compile; the resulting distroless image
# ships a single static binary at /entrypoint.
#
# Kept parameterized so a future new command (cmd/whatever) doesn't
# need a new Dockerfile; step 16's kind e2e uses this file twice
# (operator + apiserver) with different TARGET_BIN values.
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGET_BIN=cmd/operator

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o entrypoint ./${TARGET_BIN}

# distroless/static:nonroot — no shell, no libc, non-root by default.
# Every kubetest platform binary is CGO_ENABLED=0 static so this works
# for both operator + apiserver.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/entrypoint /entrypoint
USER 65532:65532

ENTRYPOINT ["/entrypoint"]

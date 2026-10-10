# Multi-purpose Go binary Dockerfile — one file used to build both the
# operator and the apiserver. `--build-arg TARGET_BIN=cmd/operator`
# selects which entrypoint to compile; the resulting distroless image
# ships a single static binary at /entrypoint.
#
# Kept parameterized so a future new command (cmd/whatever) doesn't
# need a new Dockerfile; step 16's kind e2e uses this file twice
# (operator + apiserver) with different TARGET_BIN values.
#
# `--build-arg WITH_WEB=true` (Control Center only) builds the React UI
# (web/, step 20R) in the web-true stage and embeds it; the default
# web-false stage hands over an empty dist, so operator/apiserver builds
# never pull Node. BuildKit skips the stage that isn't selected.
ARG WITH_WEB=false

FROM node:24-alpine AS web-true
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable
WORKDIR /src/web
# Dependencies first: cached until the lockfile changes. pnpm's version
# comes from package.json's packageManager field, via corepack.
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
# vite outDir: ../internal/controlcenter/webui/dist
RUN pnpm build

FROM busybox:1.37 AS web-false
RUN mkdir -p /src/internal/controlcenter/webui/dist

FROM web-${WITH_WEB} AS web

FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGET_BIN=cmd/operator

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .
COPY --from=web /src/internal/controlcenter/webui/dist/ internal/controlcenter/webui/dist/

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

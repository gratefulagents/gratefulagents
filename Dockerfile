# syntax=docker/dockerfile:1

# Build the web UI from the in-repo platform-app/ workspace.
# Pinned to the build platform: the output is static JS, so no emulation needed.
FROM --platform=$BUILDPLATFORM node:22-slim AS web-builder
RUN corepack enable && corepack prepare pnpm@10.25.0 --activate
WORKDIR /repo

# Keep dependency installation cached when only application source changes.
COPY platform-app/package.json platform-app/pnpm-lock.yaml platform-app/pnpm-workspace.yaml ./
COPY platform-app/frontend/package.json frontend/package.json
COPY platform-app/web/package.json web/package.json
COPY platform-app/tauri/package.json tauri/package.json
COPY platform-app/selfdev/package.json selfdev/package.json
# Install only the web target and its dependencies (the shared frontend);
# skips the Tauri-only desktop toolchain.
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store \
    && pnpm install --frozen-lockfile --filter web...

COPY platform-app/ ./
ARG VITE_BUILD_COMMIT=unknown
ENV VITE_BUILD_COMMIT=$VITE_BUILD_COMMIT
RUN pnpm --filter web build

# Build the manager binary. The web UI is not embedded in the Go binary; the
# dashboard serves it from DASHBOARD_WEB_DIST at runtime.
# Cross-compiled on the build platform via TARGETOS/TARGETARCH — no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
ENV GOTOOLCHAIN=local

# Download modules first for better layer caching.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    APP_VERSION="$(cat version.txt)" && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-X github.com/gratefulagents/gratefulagents/internal/buildinfo.Version=${APP_VERSION}" \
      -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=web-builder /repo/web/dist /web_dist
ENV DASHBOARD_WEB_DIST=/web_dist
USER 65532:65532

ENTRYPOINT ["/manager"]

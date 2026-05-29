# syntax=docker/dockerfile:1

# =========================================================================
# Build stage
# =========================================================================
# Pinned by digest for reproducible, supply-chain-safe builds. The tag is
# kept in the comment for readability; bump both together (Dependabot can
# track the digest). Resolve a new digest with:
#   docker buildx imagetools inspect golang:1.25-alpine
# golang:1.25-alpine (multi-arch index)
FROM golang:1.25-alpine@sha256:8d22e29d960bc50cd025d93d5b7c7d220b1ee9aa7a239b3c8f55a57e987e8d45 AS build

WORKDIR /src

# Download modules in their own layer so dependency fetches are cached
# and only re-run when go.mod / go.sum change (not on every source edit).
COPY go.mod go.sum ./
RUN go mod download

# Now bring in the rest of the source.
COPY . .

# Version string stamped into the binary (main.version). Defaults to "dev"
# for plain `docker build`; the release workflow passes the release tag via
# --build-arg VERSION=vX.Y.Z.
ARG VERSION=dev

# Build a static, stripped binary:
#   - CGO_ENABLED=0  -> no libc dependency, runs on a distroless/scratch base.
#   - -mod=readonly  -> fail if go.mod would need changes (reproducible).
#   - -trimpath      -> keep absolute build paths out of the binary.
#   - -ldflags "-s -w" -> drop the symbol table and DWARF to shrink the binary
#     (mirrors `task build`); -X stamps the version (see cmd/server/main.go).
# Caching the module and build caches across builds keeps rebuilds fast.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build -mod=readonly -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/server ./cmd/server

# =========================================================================
# Runtime stage
# =========================================================================
# distroless/static: no shell, no package manager, minimal attack surface.
# The :nonroot tag runs as uid 65532 by default. Pinned by digest; resolve
# a new one with: docker buildx imagetools inspect gcr.io/distroless/static:nonroot
# gcr.io/distroless/static:nonroot (multi-arch index)
FROM gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240

# The server loads its web assets from ./web relative to the working
# directory (os.DirFS("web") in cmd/server/main.go), so WORKDIR and the
# copied web/ directory must line up.
WORKDIR /app
COPY --from=build /out/server /app/server
COPY web /app/web

# Default listen address; override at runtime with -e ADDR=:9000.
ENV ADDR=:8080
EXPOSE 8080

USER nonroot:nonroot

# distroless has no shell, so there is no in-image HEALTHCHECK. Probe
# the built-in GET /healthz endpoint from your orchestrator instead
# (e.g. a Kubernetes liveness/readiness httpGet on /healthz:8080).
ENTRYPOINT ["/app/server"]

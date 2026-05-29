# syntax=docker/dockerfile:1

# =========================================================================
# Build stage
# =========================================================================
# Pin to the 1.25 line so the builder satisfies go.mod's `go 1.25.10`
# directive while still picking up patch updates of the base image.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Download modules in their own layer so dependency fetches are cached
# and only re-run when go.mod / go.sum change (not on every source edit).
COPY go.mod go.sum ./
RUN go mod download

# Now bring in the rest of the source.
COPY . .

# Build a static, stripped binary:
#   - CGO_ENABLED=0  -> no libc dependency, runs on a distroless/scratch base.
#   - -mod=readonly  -> fail if go.mod would need changes (reproducible).
#   - -trimpath      -> keep absolute build paths out of the binary.
#   - -ldflags "-s -w" -> drop the symbol table and DWARF to shrink the binary
#     (mirrors `task build`).
# Caching the module and build caches across builds keeps rebuilds fast.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags="-s -w" \
    -o /out/server ./cmd/server

# =========================================================================
# Runtime stage
# =========================================================================
# distroless/static: no shell, no package manager, minimal attack surface.
# The :nonroot tag runs as uid 65532 by default.
FROM gcr.io/distroless/static:nonroot

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

# syntax=docker/dockerfile:1
#
# Multi-stage build for the horreum cache server.
#
# Stage 1 (build) compiles a fully-static Go binary against the
# modules cache.  Stage 2 (runtime) is a scratch image containing
# only the binary and the timezone data needed for log timestamps.

ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Cache module dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Copy the source.
COPY . .

# Build a static binary.  CGO_ENABLED=0 + -ldflags="-s -w" produces a
# stripped binary that runs on any Linux without libc.
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETARCH
RUN CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH="${TARGETARCH:-amd64}" \
    go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /out/horreum \
        ./cmd/horreum

# Verify the build also works for the bench binary.
RUN CGO_ENABLED=0 \
    go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/horreum-bench \
        ./cmd/horreum-bench

# ──── runtime stage ────
FROM scratch AS runtime

# Timezone data for log timestamps.
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo

# The compiled binaries.
COPY --from=build /out/horreum /usr/local/bin/horreum
COPY --from=build /out/horreum-bench /usr/local/bin/horreum-bench

# A non-root user.  scratch has no /etc/passwd so we have to build one
# inline using the UID/GID macros from Docker.
# UID 1000 = horreum user, no shell needed.
COPY --from=build /etc/passwd /etc/passwd 2>/dev/null || \
    echo "horreum:x:1000:1000:horreum:/nonexistent:/sbin/nologin" > /etc/passwd

USER horreum

# Default data directory.
VOLUME ["/var/lib/horreum"]

# Expose the cache and metrics ports.
EXPOSE 7373 9090

# Default config: anonymous mode on 7373, metrics on 9090.
ENTRYPOINT ["/usr/local/bin/horreum"]
CMD ["--addr=:7373", "--transport=tcp", "--metrics-addr=:9090"]
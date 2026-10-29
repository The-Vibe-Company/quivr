# SeaweedFS 4.45 is rebuilt from the upstream tag so the published wrapper
# keeps its application version and entrypoint while receiving fixed Go
# modules. The archive and commit are recorded in deploy/seaweed/source-lock.json.
ARG SEAWEED_COMMIT=79b87202136cebdaaa7db4d94eaa5915ad381276
ARG SEAWEED_SOURCE_SHA256=d577baa0509178554b35350be261e8a6bebf440c51f0cfb1532a075cf843f1dd
ARG SEAWEED_SHORT_COMMIT=79b87202136c

FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS seaweed-builder
ARG SEAWEED_COMMIT
ARG SEAWEED_SOURCE_SHA256
ARG SEAWEED_SHORT_COMMIT
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
ENV GOMAXPROCS=2
ENV GOFLAGS=-p=2
WORKDIR /src
RUN set -eux; \
    curl -fsSL -o seaweed.tar.gz "https://github.com/seaweedfs/seaweedfs/archive/${SEAWEED_COMMIT}.tar.gz"; \
    echo "${SEAWEED_SOURCE_SHA256}  seaweed.tar.gz" | sha256sum -c -; \
    tar -xzf seaweed.tar.gz; \
    test -d "/src/seaweedfs-${SEAWEED_COMMIT}"
WORKDIR /src/seaweedfs-${SEAWEED_COMMIT}
# Go 1.27 canonicalizes the upstream go 1.26 directive while updating the
# module graph; without this spelling, go build reports go.mod edits.
# x/net v0.62.0 brings crypto, sys, term and text; text brings sync and mod.
# Go 1.27's tidy check also requires x/tools v0.51.0 from the inherited graph.
RUN set -eux; \
    go mod edit -go=1.26.0 \
      -require=golang.org/x/crypto@v0.58.0 \
      -require=golang.org/x/net@v0.62.0 \
      -require=golang.org/x/sys@v0.49.0 \
      -require=golang.org/x/text@v0.43.0 \
      -require=golang.org/x/tools@v0.51.0 \
      -require=golang.org/x/sync@v0.24.0 \
      -require=golang.org/x/mod@v0.41.0 \
      -require=golang.org/x/term@v0.47.0; \
    go mod download \
      golang.org/x/crypto@v0.58.0 \
      golang.org/x/net@v0.62.0 \
      golang.org/x/sys@v0.49.0 \
      golang.org/x/text@v0.43.0 \
      golang.org/x/tools@v0.51.0 \
      golang.org/x/sync@v0.24.0 \
      golang.org/x/mod@v0.41.0 \
      golang.org/x/term@v0.47.0; \
    mkdir -p /out; \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
      go build -trimpath \
        -ldflags "-s -w -X github.com/seaweedfs/seaweedfs/weed/util/version.COMMIT=${SEAWEED_SHORT_COMMIT}" \
        -o /out/weed ./weed

FROM chrislusf/seaweedfs:4.45@sha256:fc9f76fa993ad69966ffeb2f65d0318fcae39c6f8e20cf68ef7b3a5cb97769e5
# The base image ships Alpine's older curl/libcurl/brotli packages. Keep the pinned
# runtime image and update only those packages to the fixed v3.24 revision.
RUN apk add --no-cache --upgrade 'curl=8.23.0-r0' 'libcurl=8.23.0-r0' 'brotli-libs=1.2.1-r0'
COPY --from=seaweed-builder /out/weed /usr/bin/weed
COPY deploy/railway/seaweed-entrypoint.sh /usr/local/bin/quivr-seaweed
ENTRYPOINT ["sh", "/usr/local/bin/quivr-seaweed"]

# syntax=docker/dockerfile:1
# Build context: repository root (needs libs/gocommon).

# tree-sitter grammars are C, so this build needs cgo and a C compiler.
FROM golang:1.24-bookworm AS build
# Trust any extra CA certs in deploy/certs/ (see its README). Build stage only.
COPY deploy/certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
ENV GOWORK=off CGO_ENABLED=1
WORKDIR /src
COPY libs/gocommon libs/gocommon
COPY services/indexer/go.mod services/indexer/go.sum services/indexer/
RUN cd services/indexer && go mod download
COPY services/indexer services/indexer
RUN cd services/indexer && go build -trimpath -ldflags="-s -w" -o /out/indexer ./cmd/indexer

# Runtime needs git (to clone) and CA certificates (to clone over HTTPS).
FROM ubuntu:24.04
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --create-home indexer
COPY --from=build /out/indexer /usr/local/bin/indexer
USER indexer
ENV INDEXER_WORK_DIR=/tmp/devassist-index
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["indexer", "healthcheck"]
ENTRYPOINT ["indexer"]

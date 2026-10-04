# syntax=docker/dockerfile:1
# Build context: repository root (needs libs/gocommon).

FROM golang:1.24-alpine AS build
# Build each module on its own; go.mod's replace directive finds gocommon.
ENV GOWORK=off CGO_ENABLED=0
WORKDIR /src
# Trust any extra CA certs in deploy/certs/ (see its README). Build stage only.
COPY deploy/certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
COPY libs/gocommon libs/gocommon
COPY services/indexer/go.mod services/indexer/go.sum services/indexer/
RUN cd services/indexer && go mod download
COPY services/indexer services/indexer
RUN cd services/indexer && go build -trimpath -ldflags="-s -w" -o /out/indexer ./cmd/indexer

# distroless: no shell, no package manager, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/indexer /indexer
USER nonroot
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/indexer", "healthcheck"]
ENTRYPOINT ["/indexer"]

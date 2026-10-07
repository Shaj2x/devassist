# syntax=docker/dockerfile:1
# Build context: repository root (needs libs/gocommon).

FROM golang:1.24-alpine AS build
COPY deploy/certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
ENV GOWORK=off CGO_ENABLED=0
WORKDIR /src
COPY libs/gocommon libs/gocommon
COPY services/sandbox-runner/go.mod services/sandbox-runner/go.sum services/sandbox-runner/
RUN cd services/sandbox-runner && go mod download
COPY services/sandbox-runner services/sandbox-runner
RUN cd services/sandbox-runner && go build -trimpath -ldflags="-s -w" -o /out/sandbox-runner ./cmd/sandbox-runner

# The runner itself needs git (checkout + git apply). It talks to Docker over
# the API socket; sandboxes are separate containers, not children of this one.
FROM ubuntu:24.04
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --create-home runner
COPY --from=build /out/sandbox-runner /usr/local/bin/sandbox-runner
USER runner
ENV SANDBOX_WORK_DIR=/tmp/devassist-sandbox
EXPOSE 8081
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["sandbox-runner", "healthcheck"]
ENTRYPOINT ["sandbox-runner"]

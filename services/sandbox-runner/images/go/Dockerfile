# Sandbox image for Go repositories: toolchain + gosec + staticcheck.
FROM golang:1.24-bookworm
COPY deploy-certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true; \
    GOBIN=/usr/local/bin go install github.com/securego/gosec/v2/cmd/gosec@v2.21.4 \
 && GOBIN=/usr/local/bin go install honnef.co/go/tools/cmd/staticcheck@2025.1.1 \
 && rm -rf /root/go /root/.cache /tmp/extra-certs \
 && useradd --uid 10001 --user-group --home-dir /tmp/home --no-create-home sandbox
# Module and build caches live on the writable tmpfs.
ENV HOME=/tmp/home GOPATH=/tmp/home/go GOCACHE=/tmp/home/.cache/go-build \
    GOFLAGS=-mod=mod GOTOOLCHAIN=local CGO_ENABLED=0
USER sandbox
WORKDIR /workspace
LABEL devassist.sandbox.language=go

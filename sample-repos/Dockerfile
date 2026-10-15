# syntax=docker/dockerfile:1
# The bundled sample repos as bare git repositories, for clusters that have
# no host directory to mount. Build context: repository root.
#   docker build -f sample-repos/Dockerfile -t devassist/sample-repos:dev .
# The Kubernetes dev overlay copies /sample-repos into each pod that clones
# them (indexer, orchestrator, sandbox-runner) with an init container.

FROM ubuntu:24.04 AS publish
COPY deploy/certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY scripts/publish-sample-repos.sh scripts/
COPY sample-repos/ sample-repos/
RUN bash scripts/publish-sample-repos.sh

FROM busybox:1.37
COPY --from=publish /src/.data/sample-repos /sample-repos

# syntax=docker/dockerfile:1
# Build context: repository root (needs the uv workspace and shared lib).
#
# The orchestrator needs git (agents edit a real checkout), so both stages
# use the same Ubuntu base: the virtualenv's interpreter path then matches.

FROM ubuntu:24.04 AS base
RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 python3-venv git ca-certificates \
 && rm -rf /var/lib/apt/lists/*

FROM base AS build
# Trust any extra CA certs in deploy/certs/ (see its README). Build stage only.
COPY deploy/certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
ENV PIP_CERT=/etc/ssl/certs/ca-certificates.crt SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
RUN python3 -m venv /opt/uv && /opt/uv/bin/pip install --no-cache-dir uv==0.8.17
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON=/usr/bin/python3 UV_PYTHON_DOWNLOADS=never
WORKDIR /app
COPY pyproject.toml uv.lock ./
COPY libs/devassist-common/pyproject.toml libs/devassist-common/
COPY services/api/pyproject.toml services/api/
COPY services/orchestrator/pyproject.toml services/orchestrator/
RUN /opt/uv/bin/uv sync --frozen --no-dev --package devassist-orchestrator --no-install-workspace
COPY libs/devassist-common libs/devassist-common
COPY services/orchestrator services/orchestrator
RUN /opt/uv/bin/uv sync --frozen --no-dev --package devassist-orchestrator --no-editable

FROM base
RUN useradd --system --uid 10001 --create-home app
WORKDIR /app
COPY --from=build /app/.venv /app/.venv
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1 PORT=8001 WORK_DIR=/tmp/devassist-orchestrator
USER app
EXPOSE 8001
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD python3 -c "import urllib.request,os;urllib.request.urlopen(f'http://127.0.0.1:{os.environ[\"PORT\"]}/healthz',timeout=2)"
CMD ["devassist-orchestrator", "serve"]

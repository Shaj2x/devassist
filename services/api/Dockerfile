# syntax=docker/dockerfile:1
# Build context: repository root (needs the uv workspace and shared lib).

FROM python:3.12-slim AS build
# Trust any extra CA certs in deploy/certs/ (see its README). Build stage only.
COPY deploy/certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
ENV PIP_CERT=/etc/ssl/certs/ca-certificates.crt SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
RUN pip install --no-cache-dir uv==0.8.17
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
WORKDIR /app
# Workspace metadata first so dependency layers cache across code changes.
COPY pyproject.toml uv.lock ./
COPY libs/devassist-common/pyproject.toml libs/devassist-common/
COPY services/api/pyproject.toml services/api/
COPY services/orchestrator/pyproject.toml services/orchestrator/
RUN uv sync --frozen --no-dev --package devassist-api --no-install-workspace
COPY libs/devassist-common libs/devassist-common
COPY services/api services/api
RUN uv sync --frozen --no-dev --package devassist-api --no-editable

FROM python:3.12-slim
RUN useradd --system --uid 10001 --no-create-home app
WORKDIR /app
COPY --from=build /app/.venv /app/.venv
# Alembic config and migrations, used by the one-shot `migrate` container.
COPY services/api/alembic.ini services/api/alembic.ini
COPY services/api/alembic services/api/alembic
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1 PORT=8000
USER app
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD python -c "import urllib.request,os;urllib.request.urlopen(f'http://127.0.0.1:{os.environ[\"PORT\"]}/healthz',timeout=2)"
CMD ["python", "-m", "devassist_api"]

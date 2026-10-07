# Sandbox image for Python repositories: interpreter + test, security and
# lint tools, preinstalled so validation needs no network.
FROM python:3.12-slim
COPY deploy-certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true; \
    PIP_CERT=/etc/ssl/certs/ca-certificates.crt pip install --no-cache-dir \
      pytest==8.3.4 ruff==0.8.4 bandit==1.8.0 \
 && rm -rf /tmp/extra-certs \
 && useradd --uid 10001 --user-group --home-dir /tmp/home --no-create-home sandbox
ENV HOME=/tmp/home PYTHONUSERBASE=/tmp/home/.local PATH=/tmp/home/.local/bin:$PATH \
    PYTHONDONTWRITEBYTECODE=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
USER sandbox
WORKDIR /workspace
LABEL devassist.sandbox.language=python

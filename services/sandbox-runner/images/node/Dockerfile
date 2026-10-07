# Sandbox image for JavaScript/TypeScript repositories. eslint and tsc come
# from the repository's own devDependencies.
FROM node:22-bookworm-slim
RUN useradd --uid 10001 --user-group --home-dir /tmp/home --no-create-home sandbox
ENV HOME=/tmp/home NPM_CONFIG_CACHE=/tmp/home/.npm NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false CI=true
USER sandbox
WORKDIR /workspace
LABEL devassist.sandbox.language=node

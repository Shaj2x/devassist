# syntax=docker/dockerfile:1
# Build context: repository root.

FROM node:22-alpine AS build
# Trust any extra CA certs in deploy/certs/ (see its README). Build stage only.
COPY deploy/certs/ /tmp/extra-certs/
RUN cat /tmp/extra-certs/*.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt
WORKDIR /app
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci
COPY dashboard/ .
RUN npm run build

# nginx serves the static build and proxies /api to the FastAPI service,
# so the browser only ever talks to one origin.
FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY dashboard/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD wget -qO- http://127.0.0.1:8080/ >/dev/null || exit 1

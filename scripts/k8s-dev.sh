#!/usr/bin/env bash
# Run DevAssist on a local kind cluster with the dev overlay.
#   scripts/k8s-dev.sh up     # cluster + registry, load images, apply, wait
#   scripts/k8s-dev.sh down   # delete the cluster and registry
# Needs docker, kind and kubectl. Builds images with `make up`'s compose
# definitions first (docker compose build), so run it from the repo root.
set -euo pipefail

CLUSTER=${KIND_CLUSTER:-devassist}
REGISTRY=kind-registry
OVERLAY=deploy/k8s/overlays/dev
COMPOSE=(docker compose -f deploy/docker-compose.yml --env-file .env)

down() {
  kind delete cluster --name "$CLUSTER" || true
  docker rm -f "$REGISTRY" >/dev/null 2>&1 || true
}

up() {
  [ -f .env ] || cp .env.example .env
  [ -f "$OVERLAY/secrets.env" ] || cp "$OVERLAY/secrets.env.example" "$OVERLAY/secrets.env"

  echo "==> building images"
  "${COMPOSE[@]}" build api orchestrator indexer sandbox-runner dashboard
  make sandbox-images
  docker build -q -f sample-repos/Dockerfile -t devassist/sample-repos:dev . >/dev/null

  echo "==> kind cluster '$CLUSTER'"
  kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 120s

  # A plain registry on kind's Docker network: the runner's Docker-in-Docker
  # daemon pulls sandbox images from it (kind-registry:5000).
  if ! docker inspect "$REGISTRY" >/dev/null 2>&1; then
    docker run -d --restart=always --name "$REGISTRY" -p 127.0.0.1:5001:5000 registry:2 >/dev/null
  fi
  docker network connect kind "$REGISTRY" 2>/dev/null || true
  for lang in python go node; do
    docker tag "devassist/sandbox-$lang:latest" "localhost:5001/devassist/sandbox-$lang:latest"
    docker push -q "localhost:5001/devassist/sandbox-$lang:latest" >/dev/null
  done

  echo "==> loading app images into kind"
  for img in api orchestrator indexer sandbox-runner dashboard sample-repos; do
    kind load docker-image --name "$CLUSTER" "devassist/$img:dev" >/dev/null
  done

  echo "==> applying $OVERLAY"
  kubectl apply -k "$OVERLAY" >/dev/null
  # Make "kind-registry" resolvable inside the namespace: a selector-less
  # Service whose endpoint is the registry container on kind's network.
  registry_ip=$(docker inspect -f '{{.NetworkSettings.Networks.kind.IPAddress}}' "$REGISTRY")
  kubectl -n devassist apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Service
metadata: { name: kind-registry }
spec:
  ports: [{ name: registry, port: 5000, targetPort: 5000 }]
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: kind-registry
  labels: { kubernetes.io/service-name: kind-registry }
addressType: IPv4
ports: [{ name: registry, port: 5000 }]
endpoints: [{ addresses: ["$registry_ip"] }]
YAML
  kubectl -n devassist wait --for=condition=complete job/kafka-topics --timeout=300s
  kubectl -n devassist wait --for=condition=complete job/migrate --timeout=300s
  kubectl -n devassist rollout status deploy --timeout=600s

  cat <<MSG

DevAssist is running in kind. Reach it with:
  kubectl -n devassist port-forward svc/dashboard 3000:80   # http://localhost:3000
  kubectl -n devassist port-forward svc/api 8000:8000       # API, for make demo-api
MSG
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  *) echo "usage: $0 [up|down]" >&2; exit 2 ;;
esac

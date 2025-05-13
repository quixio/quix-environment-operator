#!/bin/bash

set -euo pipefail

SA_NAME="${1:-quix-platform-account}"
NAMESPACE="${2:-quix-operator}"

if [[ -z "$SA_NAME" ]]; then
  echo "Usage: $0 [service-account-name] [namespace]" >&2
  exit 1
fi

# Only run context validation if terminal is interactive
if [[ -t 1 ]]; then
  echo "🔍 Validating current Kubernetes context..."
  if ! kubectl cluster-info > /dev/null 2>&1; then
    echo "❌ Error: Unable to connect to current Kubernetes context." >&2
    kubectl config current-context || true
    exit 1
  fi

  CONTEXT=$(kubectl config current-context)
  echo "✅ Connected to context: $CONTEXT"
  read -rp "❓ Proceed with this context? [y/N]: " CONFIRM
  case "$CONFIRM" in
    [yY][eE][sS]|[yY]) ;;
    *) echo "❌ Aborted by user."; exit 1 ;;
  esac
fi

# Check if ServiceAccount exists
if ! kubectl get sa "$SA_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
  echo "❌ Error: ServiceAccount '$SA_NAME' not found in namespace '$NAMESPACE'" >&2
  exit 1
fi

# Generate unique secret name
BASE_SECRET_NAME="${SA_NAME}-token"
SECRET_NAME="$BASE_SECRET_NAME"
i=2
while kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; do
  SECRET_NAME="${BASE_SECRET_NAME}-$i"
  ((i++))
done

echo "🔐 Creating Secret '$SECRET_NAME' bound to ServiceAccount '$SA_NAME'..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: ${SECRET_NAME}
  namespace: ${NAMESPACE}
  annotations:
    kubernetes.io/service-account.name: ${SA_NAME}
type: kubernetes.io/service-account-token
EOF

echo "⏳ Waiting for token to be populated..."
for i in {1..10}; do
  TOKEN=$(kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" -o jsonpath='{.data.token}' 2>/dev/null || true)
  if [[ -n "$TOKEN" ]]; then
    break
  fi
  sleep 1
done

if [[ -z "$TOKEN" ]]; then
  echo "❌ Error: Token not populated in Secret '$SECRET_NAME'" >&2
  exit 1
fi

echo
echo "✅ Secret Name: $SECRET_NAME"
echo "✅ Bearer Token:"
echo "$TOKEN" | base64 -d
echo

echo "✅ Kubernetes API Server:"
kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}'
echo

echo "✅ CA Certificate (base64):"
kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" -o jsonpath='{.data.ca\.crt}'
echo

echo "🎉 Token Secret generated successfully."

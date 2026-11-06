#!/usr/bin/env bash
set -euo pipefail

# The admitted builder may use docker-container, whose cache is not a runnable image.
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
builder=()
if [[ -n "${BUILDX_BUILDER:-}" ]]; then
  builder=(--builder "$BUILDX_BUILDER")
fi
docker buildx build "${builder[@]}" --platform linux/amd64 --load \
  --iidfile "$work/image-id" -f build/dockerfile.test . --progress=plain
image=$(cat "$work/image-id")
[[ "$image" =~ ^sha256:[0-9a-f]{64}$ ]]

mkdir -p "$(pwd)/coverlet"
docker run --rm --platform linux/amd64 \
  -v "$(pwd)/coverlet":/app/coverlet "$image"

echo "All tests completed successfully"

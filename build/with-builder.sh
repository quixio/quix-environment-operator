#!/usr/bin/env bash
set -euo pipefail
: "${BUILD_BUILDID:?native producer build ID required}"
: "${EVIDENCE_DIR:?evidence directory required}"
[[ "$BUILD_BUILDID" =~ ^[1-9][0-9]*$ ]]
image='quixcontainerregistry.azurecr.io/buildkit@sha256:3a25e430255677ba5cd021511008cc4b66bc588dc03cd48071c6d447e5b2960a'
expected='sha256:028b91caccb311c2da992f286cb3cdb70350bb69d00b7471832e8769d6f4f618'
builder="operator-${BUILD_BUILDID}-$$"
mkdir -p "$EVIDENCE_DIR"
docker buildx create --name "$builder" --driver docker-container --driver-opt "image=$image"
cleanup() {
  status=$?
  trap - EXIT
  docker buildx rm "$builder" || status=1
  exit "$status"
}
trap cleanup EXIT
export BUILDX_BUILDER="$builder"
docker buildx inspect --bootstrap "$builder" | tee "$EVIDENCE_DIR/builder.log"
actual=$(docker inspect --format '{{.Image}}' "buildx_buildkit_${builder}0")
[[ "$actual" == "$expected" ]]
printf '%s\n%s\n' "$image" "$actual" > "$EVIDENCE_DIR/builder-image.txt"
"$@"

#!/usr/bin/env bash
# Push the GoReleaser zips to an OCI registry in the OpenTofu provider mirror
# layout:
#
#   <repository>:<version>          image index, artifactType application/vnd.opentofu.provider
#     one manifest per platform     artifactType application/vnd.opentofu.provider-target,
#                                   platform os/architecture, one layer archive/zip
#
# See https://opentofu.org/docs/cli/oci_registries/provider-mirror/.
# Needs ORAS 1.3.0 or later (oras manifest index create, --artifact-platform).
#
# Usage:
#   scripts/publish-oci.sh <version> <repository> [dist-dir]
#   scripts/publish-oci.sh 0.1.0 ghcr.io/znd4/opentofu-provider-desec
#
# Set DRY_RUN=1 to build the OCI layout only (in $LAYOUT, default
# ./oci-layout) and skip the push.
set -euo pipefail

version="${1:?usage: publish-oci.sh <version> <repository> [dist-dir]}"
repository="${2:?usage: publish-oci.sh <version> <repository> [dist-dir]}"
dist="$(cd "${3:-dist}" && pwd)"
version="${version#v}"
layout="$(mkdir -p "${LAYOUT:-oci-layout}" && cd "${LAYOUT:-oci-layout}" && pwd)"
source_url="${SOURCE_URL:-https://github.com/znd4/terraform-provider-desec}"

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$ ]]; then
  echo "publish-oci: version $version is not semver" >&2
  exit 1
fi
# OCI tags cannot contain "+". OpenTofu reads "_" as "+".
tag="${version//+/_}"

shopt -s nullglob
zips=("$dist"/terraform-provider-desec_"${version}"_*.zip)
if ((${#zips[@]} == 0)); then
  echo "publish-oci: no zips for version $version in $dist" >&2
  exit 1
fi

platforms=()
for zip in "${zips[@]}"; do
  file="$(basename "$zip")"
  platform="${file#terraform-provider-desec_"${version}"_}"
  platform="${platform%.zip}" # for example linux_amd64
  os="${platform%%_*}"
  arch="${platform#*_}"
  echo "publish-oci: $file -> $os/$arch" >&2
  # Run from the dist directory, so the layer title is the bare file name.
  (cd "$dist" && oras push \
    --oci-layout "$layout:$platform" \
    --artifact-type application/vnd.opentofu.provider-target \
    --artifact-platform "$os/$arch" \
    "$file:archive/zip" >/dev/null)
  platforms+=("$platform")
done

oras manifest index create \
  --oci-layout "$layout:$tag" \
  --artifact-type application/vnd.opentofu.provider \
  --annotation "org.opencontainers.image.source=$source_url" \
  --annotation "org.opencontainers.image.version=$version" \
  "${platforms[@]}" >/dev/null

# Check the index before the push.
index="$(oras manifest fetch --oci-layout "$layout:$tag")"
python3 - "$index" "${#platforms[@]}" <<'PY'
import json, sys
index = json.loads(sys.argv[1])
want = int(sys.argv[2])
assert index.get("mediaType") == "application/vnd.oci.image.index.v1+json", index.get("mediaType")
assert index.get("artifactType") == "application/vnd.opentofu.provider", index.get("artifactType")
entries = index["manifests"]
assert len(entries) == want, f"{len(entries)} entries, want {want}"
for m in entries:
    assert m.get("artifactType") == "application/vnd.opentofu.provider-target", m
    p = m.get("platform") or {}
    assert p.get("os") and p.get("architecture"), m
print(f"publish-oci: index OK, {len(entries)} platforms", file=sys.stderr)
PY

if [[ "${DRY_RUN:-}" == "1" ]]; then
  echo "publish-oci: DRY_RUN=1, not pushing. Layout: $layout:$tag" >&2
  exit 0
fi

oras cp --from-oci-layout "$layout:$tag" "$repository:$tag"
echo "publish-oci: pushed $repository:$tag" >&2

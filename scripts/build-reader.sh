#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UPSTREAM_COMMIT="41443eaba44bcbdf45d666fe27471b50d41fd994"
UPSTREAM_TAG="v20260929.01"
UPSTREAM_SHA256="768824dde35055bb2021d7023592026a4531de8175149a4c3d61c1ef45d48349"
UPSTREAM_URL="https://codeload.github.com/Suwayomi/Suwayomi-WebUI/tar.gz/${UPSTREAM_COMMIT}"

CACHE_DIR="${FK115_READER_CACHE_DIR:-${ROOT_DIR}/.cache/suwayomi-webui}"
TARBALL="${CACHE_DIR}/Suwayomi-WebUI-${UPSTREAM_COMMIT}.tar.gz"
WORK_DIR="${CACHE_DIR}/work-${UPSTREAM_COMMIT}"
OUT_DIR="${ROOT_DIR}/internal/httpserver/static/reader_dist"
HOST_DIR="${ROOT_DIR}/web/reader"

mkdir -p "${CACHE_DIR}" "${OUT_DIR}"

if [[ ! -f "${TARBALL}" ]]; then
  if [[ -f "/tmp/suwayomi-inspect/upstream.tar.gz" ]]; then
    cp "/tmp/suwayomi-inspect/upstream.tar.gz" "${TARBALL}"
  else
    echo "==> Downloading Suwayomi-WebUI ${UPSTREAM_TAG} (${UPSTREAM_COMMIT})..."
    curl -fsSL "${UPSTREAM_URL}" -o "${TARBALL}.tmp"
    mv "${TARBALL}.tmp" "${TARBALL}"
  fi
fi

ACTUAL_SHA256="$(sha256sum "${TARBALL}" | awk '{print $1}')"
if [[ "${ACTUAL_SHA256}" != "${UPSTREAM_SHA256}" ]]; then
  echo "ERROR: Suwayomi-WebUI archive SHA256 mismatch!" >&2
  echo "  expected: ${UPSTREAM_SHA256}" >&2
  echo "  actual:   ${ACTUAL_SHA256}" >&2
  rm -f "${TARBALL}"
  exit 1
fi

export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
PNPM_CMD=""
if command -v pnpm >/dev/null 2>&1; then
  PNPM_CMD="pnpm"
elif command -v corepack >/dev/null 2>&1; then
  PNPM_CMD="corepack pnpm"
else
  echo "Installing pnpm via npx..."
  PNPM_CMD="npx --yes pnpm@11.1.2"
fi

rm -rf "${WORK_DIR}"
mkdir -p "${WORK_DIR}"
tar -xzf "${TARBALL}" -C "${WORK_DIR}" --strip-components=1

echo "==> Applying Komga REST host adapter..."
python3 "${HOST_DIR}/apply-host.py" "${HOST_DIR}" "${WORK_DIR}"

echo "==> Installing Suwayomi-WebUI dependencies..."
(
  cd "${WORK_DIR}"
  ${PNPM_CMD} install --frozen-lockfile --ignore-scripts
  echo "==> Building /reader bundle..."
  ${PNPM_CMD} run build
)

echo "==> Syncing bundle to internal/httpserver/static/reader_dist..."
find "${OUT_DIR}" -mindepth 1 ! -name 'placeholder.txt' -exec rm -rf {} +
cp -R "${WORK_DIR}/build/." "${OUT_DIR}/"
cp "${HOST_DIR}/LICENSE" "${OUT_DIR}/LICENSE-Suwayomi.txt"
cat > "${OUT_DIR}/BUILD-MANIFEST.json" <<EOF
{
  "upstream": "https://github.com/Suwayomi/Suwayomi-WebUI",
  "tag": "${UPSTREAM_TAG}",
  "commit": "${UPSTREAM_COMMIT}",
  "license": "MPL-2.0"
}
EOF

echo "==> Built-in reader compiled successfully into ${OUT_DIR}"

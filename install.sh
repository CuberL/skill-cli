#!/usr/bin/env bash

set -euo pipefail

REPO="${REPO:-CuberL/skill-cli}"
VERSION="${SKILL_CLI_VERSION:-main}"
INSTALL_DIR="${INSTALL_DIR:-}"

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: required command not found: $1" >&2
    exit 1
  fi
}

resolve_install_dir() {
  if [[ -n "${INSTALL_DIR}" ]]; then
    printf '%s\n' "${INSTALL_DIR}"
    return
  fi

  if [[ -w "/usr/local/bin" ]]; then
    printf '%s\n' "/usr/local/bin"
    return
  fi

  printf '%s\n' "${HOME}/.local/bin"
}

archive_url() {
  if [[ "${VERSION}" == main ]]; then
    printf 'https://github.com/%s/archive/refs/heads/main.tar.gz\n' "${REPO}"
    return
  fi

  printf 'https://github.com/%s/archive/refs/tags/%s.tar.gz\n' "${REPO}" "${VERSION}"
}

require_cmd curl
require_cmd tar
require_cmd go

TARGET_DIR="$(resolve_install_dir)"
TMP_DIR="$(mktemp -d)"
ARCHIVE_PATH="${TMP_DIR}/skill-cli.tar.gz"

cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${TARGET_DIR}"

echo "Downloading ${REPO} (${VERSION})..."
curl -fsSL "$(archive_url)" -o "${ARCHIVE_PATH}"

echo "Extracting source..."
tar -xzf "${ARCHIVE_PATH}" -C "${TMP_DIR}"

SOURCE_DIR="$(find "${TMP_DIR}" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
if [[ -z "${SOURCE_DIR}" ]]; then
  echo "error: failed to locate extracted source directory" >&2
  exit 1
fi

echo "Building skill-cli..."
(
  cd "${SOURCE_DIR}"
  go build -o "${TMP_DIR}/skill-cli" .
)

echo "Installing to ${TARGET_DIR}/skill-cli..."
install -m 755 "${TMP_DIR}/skill-cli" "${TARGET_DIR}/skill-cli"

echo "skill-cli installed successfully."
echo "Binary: ${TARGET_DIR}/skill-cli"

case ":${PATH}:" in
  *":${TARGET_DIR}:"*) ;;
  *)
    echo "warning: ${TARGET_DIR} is not in PATH" >&2
    ;;
esac

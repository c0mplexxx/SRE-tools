#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPO_DIR="$(cd "${BOT_DIR}/.." && pwd)"

PACKAGE_NAME="${PACKAGE_NAME:-alert-list-bot}"
MAINTAINER="${MAINTAINER:-SRE Tools <sre@example.net>}"
GOOS_TARGET="${GOOS_TARGET:-linux}"
GOARCH_TARGET="${GOARCH_TARGET:-amd64}"
RELEASE="${RELEASE:-1}"

case "${GOARCH_TARGET}" in
  amd64) DEB_ARCH="${DEB_ARCH:-amd64}" ;;
  arm64) DEB_ARCH="${DEB_ARCH:-arm64}" ;;
  *) echo "unsupported GOARCH_TARGET=${GOARCH_TARGET}; set DEB_ARCH explicitly" >&2; exit 2 ;;
esac

if ! command -v go >/dev/null 2>&1; then
  echo "go is required" >&2
  exit 2
fi

if ! command -v dpkg-deb >/dev/null 2>&1; then
  echo "dpkg-deb is required; run this script on a Debian/Ubuntu builder" >&2
  exit 2
fi

if [[ -z "${VERSION:-}" ]]; then
  latest_tag="$(git -C "${REPO_DIR}" describe --tags --match 'alert-list-bot/v*' --abbrev=0 2>/dev/null || true)"
  if [[ -z "${latest_tag}" ]]; then
    echo "VERSION is required when no alert-list-bot/v* tag exists" >&2
    exit 2
  fi
  VERSION="${latest_tag#alert-list-bot/v}"
fi

DEB_VERSION="${VERSION}-${RELEASE}"
DIST_DIR="${BOT_DIR}/dist"
WORK_DIR="${DIST_DIR}/deb/${PACKAGE_NAME}_${DEB_VERSION}_${DEB_ARCH}"
PACKAGE_PATH="${DIST_DIR}/${PACKAGE_NAME}_${DEB_VERSION}_${DEB_ARCH}.deb"

rm -rf "${WORK_DIR}"
install -d "${WORK_DIR}/DEBIAN"
install -d "${WORK_DIR}/usr/local/bin"
install -d "${WORK_DIR}/lib/systemd/system"
install -d "${WORK_DIR}/usr/share/doc/${PACKAGE_NAME}/examples"

(
  cd "${BOT_DIR}"
  CGO_ENABLED=0 GOOS="${GOOS_TARGET}" GOARCH="${GOARCH_TARGET}" \
    go build -trimpath -ldflags="-s -w" -o "${WORK_DIR}/usr/local/bin/${PACKAGE_NAME}" ./cmd/alert-list-bot
)

install -m 0644 "${BOT_DIR}/deploy/alert-list-bot.service" "${WORK_DIR}/lib/systemd/system/alert-list-bot.service"
install -m 0644 "${BOT_DIR}/deploy/alert-list-bot.env.example" \
  "${WORK_DIR}/usr/share/doc/${PACKAGE_NAME}/examples/alert-list-bot.env.example"

cat > "${WORK_DIR}/DEBIAN/control" <<EOF
Package: ${PACKAGE_NAME}
Version: ${DEB_VERSION}
Section: admin
Priority: optional
Architecture: ${DEB_ARCH}
Maintainer: ${MAINTAINER}
Description: Telegram operator bot for compact Alertmanager views
 alert-list-bot polls Telegram, reads a local Alertmanager API, and renders
 compact tenant-scoped operator responses for active alerts and silences.
EOF

cat > "${WORK_DIR}/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e

if ! getent group alert-list-bot >/dev/null; then
  addgroup --system alert-list-bot >/dev/null
fi

if ! getent passwd alert-list-bot >/dev/null; then
  adduser --system --no-create-home --home /var/empty --shell /usr/sbin/nologin \
    --ingroup alert-list-bot alert-list-bot >/dev/null
fi

install -d -m 0750 -o root -g alert-list-bot /etc/alert-list-bot

if [ ! -f /etc/alert-list-bot/alert-list-bot.env ]; then
  install -m 0640 -o root -g alert-list-bot \
    /usr/share/doc/alert-list-bot/examples/alert-list-bot.env.example \
    /etc/alert-list-bot/alert-list-bot.env
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
EOF

cat > "${WORK_DIR}/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e

if [ "$1" = "remove" ] && command -v systemctl >/dev/null 2>&1; then
  systemctl stop alert-list-bot.service >/dev/null 2>&1 || true
  systemctl disable alert-list-bot.service >/dev/null 2>&1 || true
fi
EOF

chmod 0755 "${WORK_DIR}/DEBIAN/postinst" "${WORK_DIR}/DEBIAN/prerm"
dpkg-deb --build --root-owner-group "${WORK_DIR}" "${PACKAGE_PATH}"

echo "${PACKAGE_PATH}"

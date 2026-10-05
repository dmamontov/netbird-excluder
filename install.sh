#!/usr/bin/env bash
# Downloads the latest netbird-excluder release for this Mac, installs it to
# /usr/local/bin, and sets it up as a persistent LaunchDaemon. Re-run any
# time to update to the latest version - the exclusion list and running
# service are left as they are.
set -euo pipefail

REPO="dmamontov/netbird-excluder"
BIN_NAME="netbird-excluder"
INSTALL_DIR="/usr/local/bin"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "error: netbird-excluder only supports macOS" >&2
  exit 1
fi

case "$(uname -m)" in
  arm64)  asset="netbird-excluder-darwin-arm64" ;;
  x86_64) asset="netbird-excluder-darwin-amd64" ;;
  *)      echo "error: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

url="https://github.com/${REPO}/releases/latest/download/${asset}"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

echo "downloading ${url}"
curl -fsSL "$url" -o "$tmp"
chmod +x "$tmp"

echo "installing to ${INSTALL_DIR}/${BIN_NAME} (may ask for your password)"
sudo mv "$tmp" "${INSTALL_DIR}/${BIN_NAME}"
trap - EXIT

echo "setting up the LaunchDaemon"
sudo "${INSTALL_DIR}/${BIN_NAME}" install

echo
"${INSTALL_DIR}/${BIN_NAME}" list
echo
echo "installed and running. next steps:"
echo "  sudo ${BIN_NAME} add <domain|ip>    # add a domain or IPv4 address to force via LAN"
echo "  ${BIN_NAME} list                    # see current entries and their routes"
echo "  or edit /etc/netbird-excluder/config.yaml and check it with: ${BIN_NAME} validate"

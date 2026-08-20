#!/usr/bin/env bash
set -euo pipefail

REPO="heyimteee/clark"
BIN="clark"
INSTALL_DIR="/usr/local/bin"

usage() {
  cat <<EOF
Usage: curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash -s -- [opts]
   or: ./install.sh [opts]

Opts are forwarded to 'clark install':
  --yes              non-interactive (requires --ollama-model)
  --ssh HOST         remote host for separate server
  --no-docker        native run without Docker
  --ollama-model TAG model tag for --yes
  --help             this help

Examples:
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash -s -- --yes --ollama-model llama3.2
  ./install.sh --ssh 3studio-server-tail

Homebrew (macOS/Linux):
  brew install ${REPO/\//\/tap\/}/clark  # heyimteee/tap/clark
EOF
}

for arg in "$@"; do
  case "$arg" in
    --help|-h) usage; exit 0;;
  esac
done

detect_os_arch() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os" in
    Darwin) os="Darwin" ;;
    Linux) os="Linux" ;;
    *) echo "Unsupported OS: $os" >&2; exit 1;;
  esac
  case "$arch" in
    x86_64|amd64) arch="x86_64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) echo "Unsupported arch: $arch" >&2; exit 1;;
  esac
  echo "${os}_${arch}"
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

# Prefer jq, fall back to grep/sed
get_latest_tag() {
  if command -v jq >/dev/null 2>&1; then
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | jq -r .tag_name
  else
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep -o '"tag_name": *"[^"]*"' | sed -E 's/.*"([^"]+)".*/\1/'
  fi
}

main() {
  local tag os_arch url tmpdir
  os_arch="$(detect_os_arch)"

  # If clark is already built locally (git clone case), just run it
  if [[ -f "./main.go" && -f "./go.mod" ]]; then
    echo "Local checkout detected — running 'go run . install $@'"
    exec go run . install "$@"
  fi

  need_cmd curl
  need_cmd tar

  tag="$(get_latest_tag)"
  if [[ -z "$tag" || "$tag" == "null" ]]; then
    echo "Could not determine latest release tag" >&2
    exit 1
  fi
  echo "Latest release: $tag"

  url="https://github.com/${REPO}/releases/download/${tag}/${BIN}_${os_arch}.tar.gz"
  echo "Downloading $url"
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  if ! curl -fsSL "$url" -o "$tmpdir/clark.tar.gz"; then
    echo "Download failed: $url" >&2
    echo "Try: brew install heyimteee/tap/clark" >&2
    exit 1
  fi

  tar -xzf "$tmpdir/clark.tar.gz" -C "$tmpdir"
  # Archive contains ./clark binary
  local bin_path="$tmpdir/clark"
  if [[ ! -f "$bin_path" ]]; then
    # Fallback: tar may have nested dir
    bin_path="$(find "$tmpdir" -type f -name "$BIN" | head -n1)"
  fi
  if [[ -z "$bin_path" || ! -f "$bin_path" ]]; then
    echo "Binary not found in archive" >&2
    exit 1
  fi

  if [[ -w "$INSTALL_DIR" ]]; then
    mv "$bin_path" "$INSTALL_DIR/$BIN"
    chmod +x "$INSTALL_DIR/$BIN"
  else
    echo "Installing to $INSTALL_DIR/$BIN (requires sudo)"
    sudo mv "$bin_path" "$INSTALL_DIR/$BIN"
    sudo chmod +x "$INSTALL_DIR/$BIN"
  fi

  echo "Installed $INSTALL_DIR/$BIN"
  echo "Running: $BIN install $*"
  exec "$INSTALL_DIR/$BIN" install "$@"
}

main "$@"

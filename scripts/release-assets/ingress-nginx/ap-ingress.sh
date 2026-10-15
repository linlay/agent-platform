#!/usr/bin/env bash
set -euo pipefail

# Installs and maintains the nginx entry for Agent Platform instances on this
# host. Run as root. Every change is checked with `nginx -t` and rolled back
# when the check fails, so a bad edit never reaches the running nginx.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="$SCRIPT_DIR/agent-platform-ingress.conf.template"
NGINX_CONF="${AP_INGRESS_NGINX_CONF:-/etc/nginx/conf.d/agent-platform-ingress.conf}"
STATE_DIR="${AP_INGRESS_DIR:-/etc/nginx/agent-platform}"
ROUTES="$STATE_DIR/routes.map"
DOMAIN_FILE="$STATE_DIR/domain"

die() {
  echo "[ap-ingress] $*" >&2
  exit 1
}

usage() {
  cat <<'USAGE'
Usage:
  ap-ingress.sh install <domain> [--cert-dir <dir>]   instances live at <name>.<domain>
  ap-ingress.sh add <name> <port>                     route <name>.<domain> to 127.0.0.1:<port>
  ap-ingress.sh remove <name>
  ap-ingress.sh list
USAGE
}

valid_domain() {
  [[ "$1" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$ ]]
}

valid_name() {
  [[ "$1" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]
}

domain() {
  [[ -s "$DOMAIN_FILE" ]] || die "not installed; run: ap-ingress.sh install <domain>"
  cat "$DOMAIN_FILE"
}

# Replaces $1 with the content of $2, keeps the result only if nginx accepts
# the whole configuration, then reloads. Old workers keep serving their open
# WebSocket and SSE connections across the reload.
apply() {
  local target="$1" candidate="$2" backup=""
  if [[ -e "$target" ]]; then
    backup="$(mktemp "$STATE_DIR/.backup.XXXXXX")"
    cp -p "$target" "$backup"
  fi
  chmod 0644 "$candidate"
  mv "$candidate" "$target"
  if ! nginx -t >/dev/null 2>"$STATE_DIR/.nginx-test.log"; then
    if [[ -n "$backup" ]]; then
      mv "$backup" "$target"
    else
      rm -f "$target"
    fi
    cat "$STATE_DIR/.nginx-test.log" >&2
    die "nginx rejected the change; $target was restored"
  fi
  [[ -z "$backup" ]] || rm -f "$backup"
  nginx -s reload
}

command -v nginx >/dev/null 2>&1 || die "nginx is required"
action="${1:-}"
[[ -n "$action" ]] || { usage >&2; exit 2; }
shift

case "$action" in
  install)
    name="${1:-}"
    valid_domain "$name" || die "invalid domain: $name"
    shift
    cert_dir="/etc/letsencrypt/live/$name"
    if [[ "${1:-}" == "--cert-dir" ]]; then
      cert_dir="${2:-}"
      shift 2
    fi
    [[ $# -eq 0 ]] || die "unexpected argument: $1"
    [[ "$cert_dir" = /* ]] || die "--cert-dir must be absolute"
    for file in fullchain.pem privkey.pem; do
      [[ -s "$cert_dir/$file" ]] || die "missing $cert_dir/$file; issue the wildcard certificate first (see INGRESS.md)"
    done
    [[ -f "$TEMPLATE" ]] || die "missing $TEMPLATE"
    mkdir -p "$STATE_DIR" "$(dirname "$NGINX_CONF")"
    if [[ -s "$DOMAIN_FILE" && "$(cat "$DOMAIN_FILE")" != "$name" ]]; then
      die "already installed for $(cat "$DOMAIN_FILE"); remove $STATE_DIR to change the domain"
    fi
    [[ -e "$ROUTES" ]] || : >"$ROUTES"
    candidate="$(mktemp "$STATE_DIR/.conf.XXXXXX")"
    sed -e "s|__AP_DOMAIN__|$name|g" -e "s|__AP_CERT_DIR__|$cert_dir|g" \
      -e "s|__AP_ROUTES__|$ROUTES|g" "$TEMPLATE" >"$candidate"
    apply "$NGINX_CONF" "$candidate"
    printf '%s\n' "$name" >"$DOMAIN_FILE"
    echo "[ap-ingress] installed for *.$name"
    ;;
  add)
    [[ $# -eq 2 ]] || { usage >&2; exit 2; }
    name="$1" port="$2"
    valid_name "$name" || die "invalid instance name: $name"
    [[ "$port" =~ ^[0-9]{4,5}$ ]] && ((port >= 1024 && port <= 65535)) || die "invalid port: $port"
    host="$name.$(domain)"
    if grep -v "^$host " "$ROUTES" | grep -q " 127\.0\.0\.1:$port;$"; then
      die "port $port is already routed to another instance"
    fi
    candidate="$(mktemp "$STATE_DIR/.routes.XXXXXX")"
    grep -v "^$host " "$ROUTES" >"$candidate" || true
    printf '%s 127.0.0.1:%s;\n' "$host" "$port" >>"$candidate"
    apply "$ROUTES" "$candidate"
    echo "[ap-ingress] https://$host -> 127.0.0.1:$port"
    ;;
  remove)
    [[ $# -eq 1 ]] || { usage >&2; exit 2; }
    valid_name "$1" || die "invalid instance name: $1"
    host="$1.$(domain)"
    grep -q "^$host " "$ROUTES" || die "no route for $host"
    candidate="$(mktemp "$STATE_DIR/.routes.XXXXXX")"
    grep -v "^$host " "$ROUTES" >"$candidate" || true
    apply "$ROUTES" "$candidate"
    echo "[ap-ingress] removed $host"
    ;;
  list)
    domain >/dev/null
    grep -v '^#' "$ROUTES" | sed -e 's/;$//' -e '/^$/d'
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

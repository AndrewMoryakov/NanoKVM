#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# NetBird Self-Hosted Server — One-Click Deployment Script
# ============================================================================
#
# Deploys a fully working self-hosted NetBird server with:
#   - Combined netbird-server (Management + Signal + Relay + STUN + Embedded IdP)
#   - Traefik reverse proxy with automatic Let's Encrypt TLS
#   - Web Dashboard for managing peers, groups, policies
#
# Usage:
#   curl -sL <url> | bash
#   # or
#   chmod +x deploy_netbird_server.sh && ./deploy_netbird_server.sh
#
# Requirements:
#   - Linux server with public IPv4
#   - Domain/subdomain pointing to this server (A record)
#   - Ports 80, 443 (TCP) and 3478 (UDP) available
#   - Root or sudo access
#
# Tested with:
#   - Ubuntu 22.04/24.04, Debian 12
#   - docker-compose v1.29+ / Docker Compose v2+
#   - NetBird server v0.62+ (combined image with embedded IdP)
#
# ============================================================================

INSTALL_DIR="/opt/netbird"
COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
CONFIG_FILE="$INSTALL_DIR/config.yaml"
DASHBOARD_ENV="$INSTALL_DIR/dashboard.env"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()   { echo -e "${GREEN}[+]${NC} $*"; }
warn()  { echo -e "${YELLOW}[!]${NC} $*"; }
err()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }
die()   { err "$*"; exit 1; }

# ── Helpers ──────────────────────────────────────────────────────────────────

check_root() {
  if [ "$(id -u)" -ne 0 ]; then
    die "This script must be run as root (use sudo)"
  fi
}

ask() {
  local prompt="$1" default="${2:-}" value
  if [ -n "$default" ]; then
    read -rp "$(echo -e "${CYAN}$prompt${NC} [$default]: ")" value
    echo "${value:-$default}"
  else
    while true; do
      read -rp "$(echo -e "${CYAN}$prompt${NC}: ")" value
      [ -n "$value" ] && break
      warn "This field is required"
    done
    echo "$value"
  fi
}

ask_yes_no() {
  local prompt="$1" default="${2:-y}" ans
  read -rp "$(echo -e "${CYAN}$prompt${NC} [${default}]: ")" ans
  ans="${ans:-$default}"
  [[ "$ans" =~ ^[Yy] ]]
}

command_exists() { command -v "$1" >/dev/null 2>&1; }

get_public_ip() {
  curl -4 -s --connect-timeout 5 https://ifconfig.me 2>/dev/null \
    || curl -4 -s --connect-timeout 5 https://api.ipify.org 2>/dev/null \
    || curl -4 -s --connect-timeout 5 https://icanhazip.com 2>/dev/null \
    || echo "UNKNOWN"
}

get_compose_cmd() {
  if command_exists docker-compose; then
    echo "docker-compose"
  elif docker compose version >/dev/null 2>&1; then
    echo "docker compose"
  else
    echo ""
  fi
}

# Detect the Docker network name used by compose.
# docker-compose v1: <dirname>_<network>  (e.g. netbird_netbird)
# docker compose v2: <dirname>_<network>  (same convention)
get_docker_network_name() {
  local dir_name
  dir_name=$(basename "$INSTALL_DIR")
  echo "${dir_name}_netbird"
}

port_in_use() {
  local port="$1" proto="${2:-tcp}"
  if command_exists ss; then
    ss -lntu 2>/dev/null | grep -qE ":${port}\b"
  elif command_exists netstat; then
    netstat -lntu 2>/dev/null | grep -qE ":${port}\b"
  else
    return 1
  fi
}

# ── Prerequisites ────────────────────────────────────────────────────────────

check_prerequisites() {
  log "Checking prerequisites..."

  # Docker
  if ! command_exists docker; then
    warn "Docker is not installed"
    if ask_yes_no "Install Docker now?"; then
      log "Installing Docker..."
      curl -fsSL https://get.docker.com | sh
      systemctl enable docker
      systemctl start docker
      log "Docker installed: $(docker --version)"
    else
      die "Docker is required. Install it and re-run the script."
    fi
  else
    log "Docker: $(docker --version)"
  fi

  # Docker Compose
  local compose_cmd
  compose_cmd=$(get_compose_cmd)
  if [ -z "$compose_cmd" ]; then
    warn "Docker Compose is not installed"
    if ask_yes_no "Install docker-compose-plugin?"; then
      apt-get update -qq && apt-get install -y -qq docker-compose-plugin 2>/dev/null \
        || yum install -y docker-compose-plugin 2>/dev/null \
        || die "Failed to install docker-compose-plugin. Install manually."
      compose_cmd=$(get_compose_cmd)
      [ -z "$compose_cmd" ] && die "docker compose still not found after install"
    else
      die "Docker Compose is required."
    fi
  fi
  log "Compose: $($compose_cmd version 2>/dev/null || echo "$compose_cmd")"

  # openssl
  command_exists openssl || die "openssl is required (apt install openssl)"

  # curl
  command_exists curl || die "curl is required (apt install curl)"
}

# ── Gather user input ────────────────────────────────────────────────────────

gather_config() {
  echo ""
  echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
  echo -e "${CYAN}       NetBird Self-Hosted Server Configuration       ${NC}"
  echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
  echo ""

  PUBLIC_IP=$(get_public_ip)
  log "Detected public IP: $PUBLIC_IP"
  echo ""

  # Domain
  echo -e "  Enter the domain/subdomain pointing to this server."
  echo -e "  Examples: ${YELLOW}netbird.example.com${NC}, ${YELLOW}mynetbird.duckdns.org${NC}"
  echo ""
  DOMAIN=$(ask "Domain")
  DOMAIN=$(echo "$DOMAIN" | sed 's|^https\?://||; s|/$||')

  # Validate DNS
  echo ""
  log "Verifying DNS for $DOMAIN..."
  local resolved_ip
  resolved_ip=$(dig +short "$DOMAIN" 2>/dev/null | head -1)
  if [ -z "$resolved_ip" ]; then
    resolved_ip=$(getent hosts "$DOMAIN" 2>/dev/null | awk '{print $1}' | head -1)
  fi

  if [ -z "$resolved_ip" ]; then
    warn "Could not resolve $DOMAIN — make sure the DNS A record exists"
    ask_yes_no "Continue anyway?" "n" || die "Fix DNS and re-run."
  elif [ "$resolved_ip" != "$PUBLIC_IP" ]; then
    warn "$DOMAIN resolves to $resolved_ip, but this server's IP is $PUBLIC_IP"
    ask_yes_no "Continue anyway?" "n" || die "Fix DNS and re-run."
  else
    log "DNS OK: $DOMAIN → $resolved_ip"
  fi

  # Email for Let's Encrypt
  echo ""
  ACME_EMAIL=$(ask "Email for Let's Encrypt notifications" "admin@${DOMAIN}")

  # Install directory
  echo ""
  INSTALL_DIR=$(ask "Installation directory" "$INSTALL_DIR")
  COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
  CONFIG_FILE="$INSTALL_DIR/config.yaml"
  DASHBOARD_ENV="$INSTALL_DIR/dashboard.env"

  # Check existing installation
  if [ -f "$COMPOSE_FILE" ]; then
    echo ""
    warn "Existing installation found at $INSTALL_DIR"
    if ! ask_yes_no "Overwrite configuration files? (data volumes are preserved)" "n"; then
      die "Aborted. Remove or rename $INSTALL_DIR and re-run."
    fi
  fi

  # Generate secrets
  RELAY_SECRET=$(openssl rand -base64 32)
  ENCRYPT_KEY=$(openssl rand -base64 32)

  DOCKER_NETWORK=$(get_docker_network_name)
}

# ── Check & free ports ───────────────────────────────────────────────────────

check_ports() {
  log "Checking required ports..."

  for port in 80 443; do
    if port_in_use "$port"; then
      warn "Port $port is in use"

      # Try to identify what's using it
      local service_name=""
      if systemctl is-active nginx >/dev/null 2>&1; then
        service_name="nginx"
      elif systemctl is-active apache2 >/dev/null 2>&1; then
        service_name="apache2"
      elif systemctl is-active httpd >/dev/null 2>&1; then
        service_name="httpd"
      fi

      if [ -n "$service_name" ]; then
        if ask_yes_no "Stop and disable $service_name to free port $port?"; then
          systemctl stop "$service_name"
          systemctl disable "$service_name"
          log "Stopped $service_name"
        else
          die "Port $port must be free for Traefik. Stop the service using it and re-run."
        fi
      else
        die "Port $port is in use by an unknown process. Free it and re-run.\n  Check with: ss -tlnp | grep :$port"
      fi
    fi
  done

  log "Ports 80, 443 are available"
}

# ── Open firewall ────────────────────────────────────────────────────────────

configure_firewall() {
  if command_exists ufw && ufw status 2>/dev/null | grep -q "active"; then
    log "Configuring UFW firewall..."
    ufw allow 80/tcp >/dev/null
    ufw allow 443/tcp >/dev/null
    ufw allow 3478/udp >/dev/null
    log "UFW: allowed 80/tcp, 443/tcp, 3478/udp"
  elif command_exists firewall-cmd && systemctl is-active firewalld >/dev/null 2>&1; then
    log "Configuring firewalld..."
    firewall-cmd --permanent --add-port=80/tcp >/dev/null
    firewall-cmd --permanent --add-port=443/tcp >/dev/null
    firewall-cmd --permanent --add-port=3478/udp >/dev/null
    firewall-cmd --reload >/dev/null
    log "firewalld: allowed 80/tcp, 443/tcp, 3478/udp"
  else
    warn "No active firewall detected (ufw/firewalld). Make sure ports 80, 443 (TCP) and 3478 (UDP) are open."
  fi
}

# ── Write configuration files ────────────────────────────────────────────────

write_config() {
  log "Creating configuration in $INSTALL_DIR..."
  mkdir -p "$INSTALL_DIR"

  # ── config.yaml ──
  cat > "$CONFIG_FILE" << EOF
# NetBird Server Configuration (combined image with embedded IdP)
# Generated by deploy_netbird_server.sh on $(date -u +"%Y-%m-%d %H:%M UTC")
server:
  listenAddress: ":80"
  exposedAddress: "https://${DOMAIN}:443"
  stunPorts:
    - 3478
  metricsPort: 9090
  healthcheckAddress: ":9000"
  logLevel: "info"
  logFile: "console"

  authSecret: "${RELAY_SECRET}"
  dataDir: "/var/lib/netbird"

  auth:
    issuer: "https://${DOMAIN}/oauth2"
    signKeyRefreshEnabled: true
    dashboardRedirectURIs:
      - "https://${DOMAIN}/nb-auth"
      - "https://${DOMAIN}/nb-silent-auth"
    cliRedirectURIs:
      - "http://localhost:53000/"

  store:
    engine: "sqlite"
    encryptionKey: "${ENCRYPT_KEY}"
EOF

  # ── dashboard.env ──
  cat > "$DASHBOARD_ENV" << EOF
# NetBird Dashboard Configuration
# Generated by deploy_netbird_server.sh
NETBIRD_MGMT_API_ENDPOINT=https://${DOMAIN}
NETBIRD_MGMT_GRPC_API_ENDPOINT=https://${DOMAIN}
AUTH_AUDIENCE=netbird-dashboard
AUTH_CLIENT_ID=netbird-dashboard
AUTH_CLIENT_SECRET=
AUTH_AUTHORITY=https://${DOMAIN}/oauth2
USE_AUTH0=false
AUTH_SUPPORTED_SCOPES=openid profile email groups
AUTH_REDIRECT_URI=/nb-auth
AUTH_SILENT_REDIRECT_URI=/nb-silent-auth
NGINX_SSL_PORT=443
EOF

  # ── docker-compose.yml ──
  cat > "$COMPOSE_FILE" << COMPOSEEOF
version: "3.8"

networks:
  netbird:
    driver: bridge
    ipam:
      config:
        - subnet: 172.30.0.0/24

volumes:
  netbird_data:
  traefik_certs:

services:
  traefik:
    image: traefik:v3.1
    container_name: traefik
    restart: unless-stopped
    command:
      - --api=false
      - --providers.docker=true
      - --providers.docker.exposedbydefault=false
      - --entrypoints.web.address=:80
      - --entrypoints.websecure.address=:443
      - --entrypoints.web.http.redirections.entryPoint.to=websecure
      - --entrypoints.web.http.redirections.entryPoint.scheme=https
      - --certificatesresolvers.letsencrypt.acme.email=${ACME_EMAIL}
      - --certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json
      - --certificatesresolvers.letsencrypt.acme.tlschallenge=true
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - traefik_certs:/letsencrypt
    networks: [netbird]

  dashboard:
    image: netbirdio/dashboard:latest
    container_name: netbird-dashboard
    restart: unless-stopped
    networks: [netbird]
    env_file:
      - ./dashboard.env
    labels:
      - traefik.enable=true
      - traefik.docker.network=${DOCKER_NETWORK}
      - traefik.http.routers.netbird-dashboard.rule=Host(\`${DOMAIN}\`)
      - traefik.http.routers.netbird-dashboard.entrypoints=websecure
      - traefik.http.routers.netbird-dashboard.tls=true
      - traefik.http.routers.netbird-dashboard.tls.certresolver=letsencrypt
      - traefik.http.routers.netbird-dashboard.service=dashboard
      - traefik.http.routers.netbird-dashboard.priority=1
      - traefik.http.services.dashboard.loadbalancer.server.port=80

  netbird-server:
    image: netbirdio/netbird-server:latest
    container_name: netbird-server
    restart: unless-stopped
    networks: [netbird]
    ports:
      - "3478:3478/udp"
    volumes:
      - netbird_data:/var/lib/netbird
      - ./config.yaml:/etc/netbird/config.yaml
    command: ["--config", "/etc/netbird/config.yaml"]
    labels:
      - traefik.enable=true
      - traefik.docker.network=${DOCKER_NETWORK}
      # gRPC (Signal + Management)
      - traefik.http.routers.netbird-grpc.rule=Host(\`${DOMAIN}\`) && (PathPrefix(\`/signalexchange.SignalExchange/\`) || PathPrefix(\`/management.ManagementService/\`))
      - traefik.http.routers.netbird-grpc.entrypoints=websecure
      - traefik.http.routers.netbird-grpc.tls=true
      - traefik.http.routers.netbird-grpc.tls.certresolver=letsencrypt
      - traefik.http.routers.netbird-grpc.service=netbird-server-h2c
      - traefik.http.routers.netbird-grpc.priority=100
      # REST API, Relay, WebSocket, OAuth2
      - traefik.http.routers.netbird-backend.rule=Host(\`${DOMAIN}\`) && (PathPrefix(\`/relay\`) || PathPrefix(\`/ws-proxy/\`) || PathPrefix(\`/api\`) || PathPrefix(\`/oauth2\`))
      - traefik.http.routers.netbird-backend.entrypoints=websecure
      - traefik.http.routers.netbird-backend.tls=true
      - traefik.http.routers.netbird-backend.tls.certresolver=letsencrypt
      - traefik.http.routers.netbird-backend.service=netbird-server
      - traefik.http.routers.netbird-backend.priority=100
      # Services
      - traefik.http.services.netbird-server.loadbalancer.server.port=80
      - traefik.http.services.netbird-server-h2c.loadbalancer.server.port=80
      - traefik.http.services.netbird-server-h2c.loadbalancer.server.scheme=h2c
    logging:
      driver: "json-file"
      options:
        max-size: "500m"
        max-file: "2"
COMPOSEEOF

  chmod 600 "$CONFIG_FILE" "$DASHBOARD_ENV"
  log "Configuration files created"
}

# ── Launch containers ────────────────────────────────────────────────────────

launch() {
  local compose_cmd
  compose_cmd=$(get_compose_cmd)

  log "Pulling Docker images..."
  (cd "$INSTALL_DIR" && $compose_cmd pull)

  log "Starting containers..."
  (cd "$INSTALL_DIR" && $compose_cmd up -d)
}

# ── Verify deployment ────────────────────────────────────────────────────────

verify() {
  log "Waiting for services to start..."
  sleep 10

  local all_ok=true

  # Check containers
  for name in traefik netbird-dashboard netbird-server; do
    if docker ps --format '{{.Names}}' | grep -q "^${name}$"; then
      log "  $name: running"
    else
      err "  $name: NOT RUNNING"
      all_ok=false
    fi
  done

  if [ "$all_ok" = false ]; then
    echo ""
    warn "Some containers failed to start. Check logs:"
    echo "  docker logs traefik --tail 20"
    echo "  docker logs netbird-server --tail 20"
    echo "  docker logs netbird-dashboard --tail 20"
    return 1
  fi

  # Wait for TLS certificate (up to 60 seconds)
  log "Waiting for TLS certificate (Let's Encrypt)..."
  local attempts=0
  while [ $attempts -lt 12 ]; do
    if curl -sf --connect-timeout 5 "https://${DOMAIN}/oauth2/.well-known/openid-configuration" >/dev/null 2>&1; then
      log "  OIDC endpoint: OK"
      break
    fi
    attempts=$((attempts + 1))
    sleep 5
  done

  if [ $attempts -ge 12 ]; then
    warn "OIDC endpoint not responding yet. It may take a few more minutes for the TLS certificate."
    warn "Test manually: curl -s https://${DOMAIN}/oauth2/.well-known/openid-configuration"
  fi

  # Check API
  local api_response
  api_response=$(curl -s --connect-timeout 5 "https://${DOMAIN}/api/accounts" 2>/dev/null || echo "")
  if echo "$api_response" | grep -q "401\|authentication"; then
    log "  API endpoint: OK (401 — expected, no token provided)"
  else
    warn "  API endpoint: unexpected response"
  fi

  return 0
}

# ── Print summary ────────────────────────────────────────────────────────────

print_summary() {
  echo ""
  echo -e "${GREEN}═══════════════════════════════════════════════════════${NC}"
  echo -e "${GREEN}       NetBird Server Deployed Successfully!          ${NC}"
  echo -e "${GREEN}═══════════════════════════════════════════════════════${NC}"
  echo ""
  echo -e "  ${CYAN}Dashboard:${NC}       https://${DOMAIN}"
  echo -e "  ${CYAN}Management URL:${NC}  https://${DOMAIN}"
  echo -e "  ${CYAN}OIDC Discovery:${NC}  https://${DOMAIN}/oauth2/.well-known/openid-configuration"
  echo ""
  echo -e "  ${CYAN}Config directory:${NC} ${INSTALL_DIR}"
  echo -e "  ${CYAN}Config file:${NC}     ${CONFIG_FILE}"
  echo ""
  echo -e "  ${YELLOW}IMPORTANT: Save these secrets (not recoverable):${NC}"
  echo -e "    Relay secret:     ${RELAY_SECRET}"
  echo -e "    Encryption key:   ${ENCRYPT_KEY}"
  echo ""
  echo -e "  ─── Next Steps ─────────────────────────────────────"
  echo ""
  echo -e "  1. Open ${CYAN}https://${DOMAIN}${NC} in your browser"
  echo -e "     Register your admin account (email + password)"
  echo ""
  echo -e "  2. Create a Setup Key:"
  echo -e "     Dashboard → Setup Keys → Create Setup Key"
  echo -e "     (Choose 'Reusable' for multiple devices)"
  echo ""
  echo -e "  3. Connect a device:"
  echo ""
  echo -e "     ${CYAN}Windows/Mac/Linux:${NC}"
  echo -e "       netbird up --management-url https://${DOMAIN} --setup-key <KEY>"
  echo ""
  echo -e "     ${CYAN}NanoKVM Cube:${NC}"
  echo -e "       Settings → NetBird → Install → Custom Server"
  echo -e "       Management URL: https://${DOMAIN}"
  echo -e "       Setup Key: <your key>"
  echo ""
  echo -e "  ─── Useful Commands ─────────────────────────────────"
  echo ""
  echo -e "  View logs:      cd $INSTALL_DIR && $(get_compose_cmd) logs --tail 50"
  echo -e "  Restart:        cd $INSTALL_DIR && $(get_compose_cmd) restart"
  echo -e "  Update:         cd $INSTALL_DIR && $(get_compose_cmd) pull && $(get_compose_cmd) up -d"
  echo -e "  Stop:           cd $INSTALL_DIR && $(get_compose_cmd) down"
  echo ""
}

# ── Main ─────────────────────────────────────────────────────────────────────

main() {
  echo ""
  echo -e "${CYAN}╔═══════════════════════════════════════════════════════╗${NC}"
  echo -e "${CYAN}║   NetBird Self-Hosted Server — Deployment Script     ║${NC}"
  echo -e "${CYAN}║                                                       ║${NC}"
  echo -e "${CYAN}║   Installs: Management + Signal + Relay + Dashboard   ║${NC}"
  echo -e "${CYAN}║   Auth:     Embedded IdP (no external OAuth needed)   ║${NC}"
  echo -e "${CYAN}║   TLS:      Automatic via Let's Encrypt + Traefik     ║${NC}"
  echo -e "${CYAN}╚═══════════════════════════════════════════════════════╝${NC}"
  echo ""

  check_root
  check_prerequisites
  gather_config
  check_ports
  configure_firewall
  write_config
  launch
  verify
  print_summary
}

main "$@"

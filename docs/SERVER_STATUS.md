# NetBird Self-Hosted Server — Current Architecture

**Updated:** 2026-02-18
**Server version:** netbirdio/netbird-server:latest (v0.65.1, combined image)

---

## Architecture

Single combined container `netbird-server` replaces the old 4-container setup (management + signal + relay + coturn). Includes:

| Component | Function |
|-----------|----------|
| Management Server | Peer management, policies, setup keys, REST API |
| Signal Server | P2P connection coordination (gRPC) |
| Relay Server | Traffic relay when direct P2P is impossible |
| STUN Server | NAT traversal (UDP 3478) |
| Embedded IdP (Dex) | Local user authentication, no external OAuth needed |

Additional containers:

| Container | Function |
|-----------|----------|
| Traefik v3.1 | Reverse proxy, automatic Let's Encrypt TLS |
| Dashboard | Web UI for managing peers, groups, policies |

---

## Deployment

### Automated (recommended)

```bash
chmod +x scripts/deploy_netbird_server.sh
./scripts/deploy_netbird_server.sh
```

The script handles everything: Docker install, config generation, secrets, firewall, TLS, and container launch. See [Scripts](#scripts) section below.

### Manual

Follow the step-by-step guide in the external documentation:
`NETBIRD_SELFHOSTED_DEPLOYMENT.md` (in the project root, outside repo)

---

## Ports (must be open)

| Port | Protocol | Service |
|------|----------|---------|
| 80 | TCP | HTTP (Let's Encrypt ACME + redirect to 443) |
| 443 | TCP | HTTPS (Management API, Signal gRPC, Relay, Dashboard, OAuth2) |
| 3478 | UDP | STUN (NAT traversal) |

---

## Configuration Files

All files live in the install directory (default `/opt/netbird`):

| File | Description |
|------|-------------|
| `config.yaml` | Server config: listen address, auth (embedded IdP), store, secrets |
| `dashboard.env` | Dashboard OIDC settings |
| `docker-compose.yml` | Container orchestration: Traefik + Dashboard + netbird-server |

### Key config.yaml structure

```yaml
server:
  listenAddress: ":80"
  exposedAddress: "https://DOMAIN:443"
  stunPorts: [3478]
  authSecret: "<relay-secret>"
  dataDir: "/var/lib/netbird"
  auth:
    issuer: "https://DOMAIN/oauth2"       # embedded IdP
    dashboardRedirectURIs: [...]
    cliRedirectURIs: ["http://localhost:53000/"]
  store:
    engine: "sqlite"
    encryptionKey: "<aes-256-key>"
```

---

## Traefik Routing

| Route | Match Rule | Backend | Priority |
|-------|-----------|---------|----------|
| Dashboard | `Host(DOMAIN)` | dashboard:80 | 1 (lowest) |
| gRPC | `PathPrefix(/signalexchange., /management.)` | netbird-server:80 (h2c) | 100 |
| Backend | `PathPrefix(/relay, /ws-proxy/, /api, /oauth2)` | netbird-server:80 | 100 |
| STUN | UDP :3478 | netbird-server:3478 (direct, not via Traefik) | — |

**Important:** Traefik label `traefik.docker.network` must match the actual Docker network name. With docker-compose v1 in directory `netbird`, the network is `netbird_netbird`.

---

## Scripts

### `scripts/deploy_netbird_server.sh`

One-click deployment script for a fresh server. Features:

- Checks/installs Docker and Docker Compose
- Asks for domain (validates DNS resolution against server IP)
- Asks for Let's Encrypt email
- Generates cryptographic secrets (`openssl rand -base64 32`)
- Detects and stops conflicting services (nginx, apache)
- Configures firewall (ufw or firewalld)
- Creates config.yaml, dashboard.env, docker-compose.yml
- Pulls images and starts containers
- Verifies OIDC endpoint and API

Usage:
```bash
./scripts/deploy_netbird_server.sh
```

### `scripts/build_cube_overlay.sh`

Builds NanoKVM Cube firmware overlay (backend + frontend + init scripts).

Usage:
```bash
./scripts/build_cube_overlay.sh                    # full build
./scripts/build_cube_overlay.sh --skip-backend     # frontend only
./scripts/build_cube_overlay.sh --skip-frontend    # backend only
```

Requires: WSL with RISC-V cross-compiler, Go, Node.js 20+, pnpm.

### `scripts/deploy_cube.sh`

Deploys built overlay to NanoKVM Cube via SSH.

---

## Known Issues & Workarounds

### 1. NetBird iptables ACL bug on RISC-V

**Problem:** NetBird firewall manager does not populate `NETBIRD-ACL-INPUT` iptables chain on RISC-V, causing all tunnel traffic to be dropped.

**Workaround:** `S99netbird` init script injects `iptables -I NETBIRD-ACL-INPUT -s 100.96.0.0/16 -j ACCEPT` after daemon starts.

**Details:** See `NETBIRD_IPTABLES_ISSUE.md`

### 2. NanoKVM Cube has no RTC battery

**Problem:** Clock resets on power loss. TLS connections to NetBird server fail (cert "not yet valid").

**Workaround:** `S95nanokvm` syncs clock via NTP before starting VPN services.

### 3. Daemon socket race condition

**Problem:** After `ResetToOfficialServer()`, old daemon socket persists briefly, causing false-positive `WaitForSocket()`.

**Fix:** `cli.go` includes `waitForSocketRemoval(5s)` between Stop() and Start(), with 30s socket timeout.

---

## Management Commands

```bash
# SSH to server
ssh root@SERVER_IP

# Container status
docker ps --format 'table {{.Names}}\t{{.Status}}'

# Server logs
docker logs netbird-server --tail 50
docker logs traefik --tail 20

# Check OIDC
curl -s https://DOMAIN/oauth2/.well-known/openid-configuration | head -5

# Check API (expect 401)
curl -s https://DOMAIN/api/accounts

# Restart
cd /opt/netbird && docker compose restart

# Update
cd /opt/netbird && docker compose pull && docker compose up -d

# Backup data
docker run --rm -v netbird_netbird_data:/data -v /root/backup:/backup \
  alpine tar czf /backup/netbird-data-$(date +%F).tar.gz -C /data .
```

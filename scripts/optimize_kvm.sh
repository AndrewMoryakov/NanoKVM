#!/usr/bin/env bash
# NanoKVM Cube — interactive optimization & maintenance script
# Runs on the local machine, connects to KVM via SSH.
# Usage: bash optimize_kvm.sh [--host HOST] [--user USER] [--port PORT] [--pass PASSWORD]
set -euo pipefail

# ── defaults ──────────────────────────────────────────────────
DEFAULT_HOST="192.168.0.36"
DEFAULT_USER="root"
DEFAULT_PORT="22"
DEFAULT_PASS=""
SSH_COMMON="-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o ServerAliveInterval=5 -o ServerAliveCountMax=4"

# ── colors ────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; NC='\033[0m'
ok()   { echo -e "${GREEN}[OK]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
err()  { echo -e "${RED}[ERROR]${NC} $*" >&2; }
info() { echo -e "${CYAN}[INFO]${NC} $*"; }

# ── argument parsing ──────────────────────────────────────────
HOST="$DEFAULT_HOST"
USER_NAME="$DEFAULT_USER"
PORT="$DEFAULT_PORT"
PASS="$DEFAULT_PASS"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) HOST="${2:-}"; shift 2 ;;
    --user) USER_NAME="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    --pass) PASS="${2:-}"; shift 2 ;;
    *) err "Unknown argument: $1"; exit 1 ;;
  esac
done

# ── interactive prompt for missing values ─────────────────────
read -rp "Host [$HOST]: " inp; [ -n "$inp" ] && HOST="$inp"
read -rp "User [$USER_NAME]: " inp; [ -n "$inp" ] && USER_NAME="$inp"
read -rp "SSH port [$PORT]: " inp; [ -n "$inp" ] && PORT="$inp"
if [ -z "$PASS" ]; then
  read -rsp "Password (leave empty for key auth): " PASS; echo
fi

REMOTE="$USER_NAME@$HOST"
SSH_OPTS="-p $PORT $SSH_COMMON"

# ── SSH helper ────────────────────────────────────────────────
run_ssh() {
  if [ -n "$PASS" ]; then
    sshpass -p "$PASS" ssh $SSH_OPTS "$REMOTE" "$@"
  else
    ssh $SSH_OPTS "$REMOTE" "$@"
  fi
}

run_ssh_script() {
  # pipe a heredoc-style script via stdin
  if [ -n "$PASS" ]; then
    sshpass -p "$PASS" ssh $SSH_OPTS "$REMOTE" "sh -s"
  else
    ssh $SSH_OPTS "$REMOTE" "sh -s"
  fi
}

# ── connectivity check ────────────────────────────────────────
info "Testing SSH connection to $REMOTE:$PORT ..."
if ! run_ssh "echo SSH_OK" >/dev/null 2>&1; then
  err "Cannot connect to $REMOTE:$PORT"
  exit 1
fi
ok "SSH connection established"
echo

# ═══════════════════════════════════════════════════════════════
# Menu
# ═══════════════════════════════════════════════════════════════
show_menu() {
  echo "========================================"
  echo " NanoKVM Cube — Optimize & Maintain"
  echo " Target: $REMOTE"
  echo "========================================"
  echo " 1. Show system summary"
  echo " 2. Fix NetBird iptables (disable broken firewall manager)"
  echo " 3. Clean /root leftovers (old backups, .deb, extracted files)"
  echo " 4. Clean /boot artifacts (System Volume Information)"
  echo " 5. Remount /boot (clear dirty flag)"
  echo " 6. Truncate logs"
  echo " 7. Show disk usage"
  echo " 8. Show iptables rules"
  echo " 9. Run ALL optimizations (2-6)"
  echo " 0. Exit"
  echo "========================================"
}

# ── 1. System summary ────────────────────────────────────────
do_summary() {
  info "Fetching system summary..."
  run_ssh_script <<'REMOTE'
echo "── System ──"
uname -a
uptime
echo
echo "── Memory ──"
free 2>/dev/null || cat /proc/meminfo | head -5
echo
echo "── Disk ──"
df -h / /boot /tmp 2>/dev/null
echo
echo "── Services ──"
printf "NanoKVM-Server: "; pgrep -x NanoKVM-Server >/dev/null 2>&1 && echo "running" || echo "NOT running"
printf "kvm_system:     "; pgrep -x kvm_system >/dev/null 2>&1 && echo "running" || echo "NOT running"
printf "netbird:        "; pgrep -x netbird >/dev/null 2>&1 && echo "running" || echo "NOT running"
printf "tailscaled:     "; pgrep -x tailscaled >/dev/null 2>&1 && echo "running" || echo "NOT running"
printf "sshd:           "; pgrep -x sshd >/dev/null 2>&1 && echo "running" || echo "NOT running"
echo
echo "── Network ──"
ip addr show eth0 2>/dev/null | grep "inet "
ip addr show wt0 2>/dev/null | grep "inet " || true
echo
echo "── Listening ports ──"
netstat -lntp 2>/dev/null || true
REMOTE
}

# ── 2. Fix NetBird iptables ──────────────────────────────────
do_fix_netbird() {
  info "Checking NetBird firewall..."
  run_ssh_script <<'REMOTE'
NB_CONFIG="/var/lib/netbird/default.json"

if [ ! -f "$NB_CONFIG" ]; then
  echo "NetBird config not found — skipping"
  exit 0
fi

if grep -q '"DisableFirewall": true' "$NB_CONFIG"; then
  echo "NetBird firewall already disabled — OK"
  # Check if old chains still linger
  if iptables -L NETBIRD-ACL-INPUT -n >/dev/null 2>&1; then
    echo "Stale NETBIRD-ACL-INPUT chain found — restarting NetBird to clean up..."
    /etc/init.d/S99netbird restart 2>&1
    sleep 3
  fi
else
  echo "Setting DisableFirewall=true in $NB_CONFIG..."
  cp "$NB_CONFIG" "${NB_CONFIG}.bak"
  sed -i 's/"DisableFirewall": false/"DisableFirewall": true/' "$NB_CONFIG"
  echo "Restarting NetBird..."
  /etc/init.d/S99netbird stop 2>&1
  sleep 2
  /etc/init.d/S99netbird start 2>&1
  sleep 3
fi

echo
echo "Verify:"
if iptables -L NETBIRD-ACL-INPUT -n >/dev/null 2>&1; then
  echo "  [WARN] NETBIRD-ACL-INPUT chain still exists"
  iptables -L NETBIRD-ACL-INPUT -n -v
else
  echo "  [OK] No NETBIRD-ACL-INPUT chain (firewall disabled)"
fi

netbird status 2>&1 | grep -E "^(Management|Signal|Peers)" || true
REMOTE
}

# ── 3. Clean /root ───────────────────────────────────────────
do_clean_root() {
  info "Scanning /root for cleanable items..."
  # First show what would be cleaned
  ITEMS=$(run_ssh_script <<'REMOTE'
found=0
for f in \
  /root/kvmapp.backup.* \
  /root/*.deb \
  /root/data.tar.xz \
  /root/zero \
  /root/pool.ntp.org \
  /root/control.tar.xz \
  /root/debian-binary \
; do
  if [ -e "$f" ]; then
    sz=$(du -sh "$f" 2>/dev/null | cut -f1)
    echo "  $sz  $f"
    found=1
  fi
done
# Check for extracted package dirs
for d in /root/usr /root/etc /root/lib /root/var; do
  if [ -d "$d" ]; then
    sz=$(du -sh "$d" 2>/dev/null | cut -f1)
    echo "  $sz  $d/"
    found=1
  fi
done
[ "$found" = "0" ] && echo "CLEAN"
REMOTE
)

  if [ "$ITEMS" = "CLEAN" ]; then
    ok "/root is clean — nothing to remove"
    return
  fi

  echo "Found cleanable items:"
  echo "$ITEMS"
  echo
  read -rp "Remove these items? [y/N]: " confirm
  if [[ "$confirm" =~ ^[Yy]$ ]]; then
    run_ssh_script <<'REMOTE'
rm -rf /root/kvmapp.backup.* 2>/dev/null
rm -f /root/*.deb /root/data.tar.xz /root/control.tar.xz /root/debian-binary 2>/dev/null
rm -rf /root/zero /root/pool.ntp.org 2>/dev/null
rm -rf /root/usr /root/etc /root/lib /root/var 2>/dev/null
echo "Cleaned. /root usage: $(du -sh /root 2>/dev/null | cut -f1)"
REMOTE
    ok "Cleanup done"
  else
    info "Skipped"
  fi
}

# ── 4. Clean /boot ───────────────────────────────────────────
do_clean_boot() {
  info "Checking /boot for artifacts..."
  run_ssh_script <<'REMOTE'
if [ -d "/boot/System Volume Information" ]; then
  rm -rf "/boot/System Volume Information"
  echo "[OK] Removed /boot/System Volume Information"
else
  echo "[OK] No Windows artifacts in /boot"
fi
df -h /boot
REMOTE
}

# ── 5. Remount /boot ─────────────────────────────────────────
do_remount_boot() {
  info "Remounting /boot to clear dirty flag..."
  run_ssh_script <<'REMOTE'
mount -o remount /boot 2>&1 && echo "[OK] /boot remounted" || echo "[WARN] Remount failed"
REMOTE
}

# ── 6. Truncate logs ─────────────────────────────────────────
do_truncate_logs() {
  info "Checking log sizes..."
  BEFORE=$(run_ssh "du -sh /var/log/netbird/client.log /tmp/messages 2>/dev/null || true")
  echo "$BEFORE"
  echo
  read -rp "Truncate these logs? [y/N]: " confirm
  if [[ "$confirm" =~ ^[Yy]$ ]]; then
    run_ssh_script <<'REMOTE'
: > /var/log/netbird/client.log 2>/dev/null && echo "[OK] Truncated netbird client.log" || true
: > /var/log/netbird/netbird.log 2>/dev/null && echo "[OK] Truncated netbird.log" || true
: > /tmp/messages 2>/dev/null && echo "[OK] Truncated /tmp/messages" || true
REMOTE
    ok "Logs truncated"
  else
    info "Skipped"
  fi
}

# ── 7. Disk usage ────────────────────────────────────────────
do_disk() {
  info "Disk usage:"
  run_ssh_script <<'REMOTE'
echo "── Filesystems ──"
df -h
echo
echo "── Large directories ──"
du -sh /kvmapp /tmp /data /root /var/log /boot 2>/dev/null | sort -rh
REMOTE
}

# ── 8. Iptables ──────────────────────────────────────────────
do_iptables() {
  info "Current iptables rules:"
  run_ssh "iptables -L -n -v 2>&1"
}

# ── 9. Run all ───────────────────────────────────────────────
do_all() {
  echo
  do_fix_netbird
  echo
  do_clean_root
  echo
  do_clean_boot
  echo
  do_remount_boot
  echo
  do_truncate_logs
  echo
  ok "All optimizations complete"
}

# ── Main loop ─────────────────────────────────────────────────
while true; do
  echo
  show_menu
  read -rp "Select [0-9]: " choice
  echo
  case "$choice" in
    1) do_summary ;;
    2) do_fix_netbird ;;
    3) do_clean_root ;;
    4) do_clean_boot ;;
    5) do_remount_boot ;;
    6) do_truncate_logs ;;
    7) do_disk ;;
    8) do_iptables ;;
    9) do_all ;;
    0) ok "Bye"; exit 0 ;;
    *) warn "Invalid choice" ;;
  esac
done

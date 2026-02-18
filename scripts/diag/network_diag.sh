#!/bin/bash
# Network diagnostics from WSL — checks connectivity to KVM and local network.
# Usage: bash network_diag.sh [KVM_IP]
set -euo pipefail

KVM_IP="${1:-192.168.0.36}"
GATEWAY="192.168.0.1"

echo "=== WSL network config ==="
ip addr show eth0 2>/dev/null | head -6
echo
ip route show 2>/dev/null

echo ""
echo "=== Ping tests ==="
for target in "$GATEWAY" "$KVM_IP" 8.8.8.8; do
  printf "%-18s " "$target:"
  result=$(ping -c 3 -W 2 "$target" 2>&1 | tail -1)
  if echo "$result" | grep -q "rtt"; then
    echo "OK  $result"
  else
    loss=$(ping -c 3 -W 2 "$target" 2>&1 | grep "packet loss" || echo "100% packet loss")
    echo "FAIL  $loss"
  fi
done

echo ""
echo "=== Port check on $KVM_IP ==="
for port in 22 80 443 8080; do
  timeout 3 bash -c "echo >/dev/tcp/$KVM_IP/$port" 2>/dev/null \
    && echo "Port $port: OPEN" || echo "Port $port: closed/filtered"
done

echo ""
echo "=== SSH test ==="
timeout 5 ssh -v -o StrictHostKeyChecking=no -o ConnectTimeout=5 \
  -o PasswordAuthentication=yes root@"$KVM_IP" 'echo SSH_OK' 2>&1 | \
  grep -E "SSH_OK|Connection|Authenticated|connect to" || echo "SSH failed"

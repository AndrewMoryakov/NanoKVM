#!/bin/bash
# Scan local network for alive hosts, SSH and HTTP services.
# Usage: bash scan_lan.sh [SUBNET]
# Example: bash scan_lan.sh 192.168.0
set -euo pipefail

SUBNET="${1:-192.168.0}"

echo "=== Port scan of ${SUBNET}.36 (common NanoKVM ports) ==="
for port in 22 80 443 2222 3000 4000 5000 8080 8443 9000 9090 9100; do
  timeout 2 bash -c "echo >/dev/tcp/${SUBNET}.36/$port" 2>/dev/null \
    && echo "Port $port: OPEN" || echo "Port $port: closed"
done

echo ""
echo "=== Full /${SUBNET}.0/24 ping sweep ==="
for i in $(seq 1 254); do
  ping -c 1 -W 1 "${SUBNET}.$i" >/dev/null 2>&1 && echo "${SUBNET}.$i: ALIVE" &
done
wait
echo "Done ping sweep"

echo ""
echo "=== SSH scan (port 22) ==="
for i in $(seq 1 254); do
  timeout 1 bash -c "echo >/dev/tcp/${SUBNET}.$i/22" 2>/dev/null \
    && echo "${SUBNET}.$i:22 OPEN" &
done
wait
echo "Done SSH scan"

echo ""
echo "=== HTTP scan (port 80) ==="
for i in $(seq 1 254); do
  timeout 1 bash -c "echo >/dev/tcp/${SUBNET}.$i/80" 2>/dev/null \
    && echo "${SUBNET}.$i:80 OPEN" &
done
wait
echo "Done HTTP scan"

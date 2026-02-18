#!/bin/sh
# NetBird diagnostics — run on KVM via SSH.
# Collects memory, config, status, logs, iptables.

echo "=== NetBird version ==="
netbird version 2>&1 || echo "netbird not found"

echo
echo "=== NetBird process memory ==="
NB_PID=$(pidof netbird 2>/dev/null)
if [ -n "$NB_PID" ]; then
  echo "PID: $NB_PID"
  grep -E "Vm|Threads" /proc/$NB_PID/status 2>/dev/null
else
  echo "NetBird is not running"
fi

echo
echo "=== All Go processes memory ==="
for proc in netbird NanoKVM-Server tailscaled; do
  pid=$(pidof $proc 2>/dev/null)
  if [ -n "$pid" ]; then
    rss=$(grep VmRSS /proc/$pid/status 2>/dev/null | awk '{print $2}')
    swap=$(grep VmSwap /proc/$pid/status 2>/dev/null | awk '{print $2}')
    echo "$proc (PID $pid): RSS=${rss}kB Swap=${swap}kB"
  fi
done

echo
echo "=== System memory ==="
free 2>/dev/null || cat /proc/meminfo | head -5

echo
echo "=== NetBird config ==="
cat /var/lib/netbird/default.json 2>/dev/null || echo "config not found"

echo
echo "=== NetBird status (detailed) ==="
netbird status --detail 2>&1 || echo "could not get status"

echo
echo "=== iptables (check for NETBIRD chains) ==="
iptables -L -n -v 2>&1

echo
echo "=== IPv6 on wt0 ==="
ip -6 addr show wt0 2>/dev/null || echo "no ipv6 on wt0"

echo
echo "=== WireGuard interface stats ==="
ip -s link show wt0 2>/dev/null || echo "wt0 not found"

echo
echo "=== NetBird log stats ==="
ls -lh /var/log/netbird/ 2>/dev/null || echo "no log dir"
wc -l /var/log/netbird/client.log 2>/dev/null || true

echo
echo "=== Last 30 lines of client.log ==="
tail -30 /var/log/netbird/client.log 2>/dev/null || true

echo
echo "=== NetBird connections ==="
netstat -tnp 2>/dev/null | grep netbird | head -20 || true

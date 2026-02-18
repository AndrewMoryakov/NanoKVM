#!/bin/sh
echo "=== Firmware version ==="
cat /kvmapp/version 2>/dev/null || echo "no version file"

echo
echo "=== Services ==="
printf "NanoKVM-Server: "; pgrep -x NanoKVM-Server >/dev/null 2>&1 && echo "running" || echo "NOT_RUNNING"
printf "kvm_system:     "; pgrep -x kvm_system >/dev/null 2>&1 && echo "running" || echo "NOT_RUNNING"
printf "netbird:        "; pgrep -x netbird >/dev/null 2>&1 && echo "running" || echo "NOT_RUNNING"
printf "sshd:           "; pgrep -x sshd >/dev/null 2>&1 && echo "running" || echo "NOT_RUNNING"

echo
echo "=== NetBird config check ==="
grep -E "DisableFirewall|DisableIPv6|LazyConnection|DisableDNS" /var/lib/netbird/default.json 2>/dev/null

echo
echo "=== NetBird status ==="
netbird status 2>&1 | head -15

echo
echo "=== iptables (no NETBIRD chains expected) ==="
iptables -L NETBIRD-ACL-INPUT -n 2>&1 || echo "(chain does not exist - OK)"

echo
echo "=== Web UI ==="
wget -q -O - http://127.0.0.1 2>/dev/null | head -1 || echo "web UI not responding"

echo
echo "=== Memory ==="
free
NB_PID=$(pidof netbird 2>/dev/null)
if [ -n "$NB_PID" ]; then
  rss=$(grep VmRSS /proc/$NB_PID/status | awk '{print $2}')
  echo "NetBird RSS: ${rss} kB"
fi

echo
echo "=== S99netbird GOMEMLIMIT ==="
grep GOMEMLIMIT /etc/init.d/S99netbird 2>/dev/null | head -2

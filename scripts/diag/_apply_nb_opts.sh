#!/bin/sh
# Apply NetBird config optimizations on live KVM device.
set -eu

NB_CONFIG="/var/lib/netbird/default.json"

if [ ! -f "$NB_CONFIG" ]; then
  echo "NetBird config not found at $NB_CONFIG"
  exit 1
fi

echo "=== Before ==="
grep -E "DisableIPv6|LazyConnection|DisableDNS|DisableFirewall|GOMEMLIMIT" "$NB_CONFIG" 2>/dev/null || true
echo "GOMEMLIMIT in S99netbird: $(grep GOMEMLIMIT /etc/init.d/S99netbird 2>/dev/null | head -1)"

echo
echo "=== Applying config changes ==="
cp "$NB_CONFIG" "${NB_CONFIG}.pre-optimize"

# DisableFirewall (already set, ensure it stays)
sed -i 's/"DisableFirewall": false/"DisableFirewall": true/' "$NB_CONFIG"

# DisableIPv6Discovery — no IPv6 on wt0, saves ICE overhead
sed -i 's/"DisableIPv6Discovery": false/"DisableIPv6Discovery": true/' "$NB_CONFIG"

# LazyConnectionEnabled — connect only on actual traffic, saves CPU/RAM for offline peers
sed -i 's/"LazyConnectionEnabled": false/"LazyConnectionEnabled": true/' "$NB_CONFIG"

# DisableDNS — not using NetBird DNS routing
sed -i 's/"DisableDNS": false/"DisableDNS": true/' "$NB_CONFIG"

echo
echo "=== After ==="
grep -E "DisableIPv6|LazyConnection|DisableDNS|DisableFirewall" "$NB_CONFIG"

echo
echo "=== Restarting NetBird ==="
/etc/init.d/S99netbird stop 2>&1
sleep 2
/etc/init.d/S99netbird start 2>&1
sleep 4

echo
echo "=== Verify ==="
netbird status 2>&1 | grep -E "^(Management|Signal|Relays|Peers|Lazy)"

NB_PID=$(pidof netbird 2>/dev/null)
if [ -n "$NB_PID" ]; then
  rss=$(grep VmRSS /proc/$NB_PID/status 2>/dev/null | awk '{print $2}')
  echo "NetBird RSS: ${rss} kB"
fi

echo
echo "Done"

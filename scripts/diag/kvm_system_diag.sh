#!/bin/sh
# System diagnostics — run on KVM via SSH.
# Comprehensive check of system health, services, storage, network.

echo "=== System ==="
uname -a
uptime
cat /etc/os-release 2>/dev/null | grep PRETTY_NAME || true

echo
echo "=== Memory ==="
free 2>/dev/null || cat /proc/meminfo | head -5
echo
echo "Top memory consumers:"
ps -o pid,vsz,rss,comm 2>/dev/null | sort -k3 -rn | head -10 || \
  ps | head -15

echo
echo "=== Storage ==="
df -h / /boot /tmp 2>/dev/null
echo
echo "Large directories:"
du -sh /kvmapp /tmp /data /root /var/log /boot 2>/dev/null | sort -rh

echo
echo "=== Services ==="
printf "NanoKVM-Server: "; pgrep -x NanoKVM-Server >/dev/null 2>&1 && echo "running (PID $(pidof NanoKVM-Server))" || echo "NOT running"
printf "kvm_system:     "; pgrep -x kvm_system >/dev/null 2>&1 && echo "running (PID $(pidof kvm_system))" || echo "NOT running"
printf "netbird:        "; pgrep -x netbird >/dev/null 2>&1 && echo "running (PID $(pidof netbird))" || echo "NOT running"
printf "tailscaled:     "; pgrep -x tailscaled >/dev/null 2>&1 && echo "running (PID $(pidof tailscaled))" || echo "NOT running"
printf "sshd:           "; pgrep -x sshd >/dev/null 2>&1 && echo "running" || echo "NOT running"
printf "ntpd:           "; pgrep -x ntpd >/dev/null 2>&1 && echo "running" || echo "NOT running"

echo
echo "=== Network interfaces ==="
ip addr show 2>/dev/null | grep -E "^[0-9]|inet "

echo
echo "=== Listening ports ==="
netstat -lntp 2>/dev/null || true

echo
echo "=== iptables summary ==="
echo "INPUT policy: $(iptables -L INPUT -n 2>/dev/null | head -1)"
iptables -L -n 2>&1 | grep -E "^Chain|ACCEPT|DROP|REJECT" | head -20

echo
echo "=== VPN preference ==="
printf "VPN setting: "; cat /etc/kvm/vpn 2>/dev/null || echo "(not set)"

echo
echo "=== Kernel errors (last 20) ==="
dmesg 2>/dev/null | grep -iE "error|fail|warn|oom|panic" | tail -20

echo
echo "=== Date/time ==="
date
echo "RTC:"; hwclock 2>/dev/null || cat /proc/driver/rtc 2>/dev/null || echo "no hwclock"

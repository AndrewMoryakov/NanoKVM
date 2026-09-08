#!/bin/sh
# Regression coverage for the VPN init-script guards and S95 boot selection.
# Run with: sh kvmapp/system/init.d/tests/test_vpn_init_scripts.sh
#
# This intentionally uses only POSIX shell and small command shims so it also
# runs with BusyBox ash.  Scripts are copied into a temporary root rather than
# touching the host's /etc, /usr, or /kvmapp.
set -eu

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/../../../.." && pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/nanokvm-vpn-init.XXXXXX")
REAL_MV=$(command -v mv)

cleanup() {
    rm -rf "$WORK"
}
trap cleanup 0 HUP INT TERM

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

new_case() {
    CASE="$WORK/$1"
    mkdir -p "$CASE/bin" "$CASE/etc/init.d" "$CASE/etc/kvm" \
        "$CASE/usr/bin" "$CASE/usr/sbin" "$CASE/kvmapp/system/init.d" \
        "$CASE/kvmapp/system/netbird" "$CASE/var/run" "$CASE/var/log/netbird"

    cat > "$CASE/bin/pidof" <<'EOF'
#!/bin/sh
: "${PIDOF_LOG:?}"
printf '%s\n' "$*" >> "$PIDOF_LOG"
exit 1
EOF
    chmod 755 "$CASE/bin/pidof"

    # The symlink restart cases deliberately get as far as the start branch.
    # Never let that fixture launch a process or wait on a host daemon.
    cat > "$CASE/bin/start-stop-daemon" <<'EOF'
#!/bin/sh
exit 1
EOF
    chmod 755 "$CASE/bin/start-stop-daemon"

    # Fail only the staged-S99 -> active-S99 publish.  The preceding S98 stage
    # and the subsequent rollback use the real mv, exercising the transaction.
    {
        printf '%s\n' '#!/bin/sh'
        printf '%s\n' 'if [ "${FAIL_NETBIRD_PUBLISH:-0}" = 1 ] && [ "${3:-}" = "${PUBLISH_DEST:-}" ]; then'
        printf '%s\n' '    case "${2:-}" in */.S99netbird.new.*) exit 1 ;; esac'
        printf '%s\n' 'fi'
        printf '%s\n' 'if [ "${FAIL_TAILSCALE_ROLLBACK:-0}" = 1 ] && [ "${3:-}" = "${ROLLBACK_DEST:-}" ]; then'
        printf '%s\n' '    case "${2:-}" in */.S98tailscaled.old.*) exit 1 ;; esac'
        printf '%s\n' 'fi'
        printf 'exec %s "$@"\n' "$REAL_MV"
    } > "$CASE/bin/mv"
    chmod 755 "$CASE/bin/mv"
}

make_executable_file() {
    printf '%s\n' '#!/bin/sh' 'exit 0' > "$1"
    chmod 755 "$1"
}

make_symlink() {
    target="$1"
    link="$2"
    ln -s "$target" "$link" 2>/dev/null && [ -L "$link" ]
}

prepare_s95() {
    cp "$REPO_ROOT/kvmapp/system/init.d/S99netbird" \
        "$CASE/kvmapp/system/init.d/S99netbird"
    printf '%s\n' '1.2.3' > "$CASE/kvmapp/system/netbird/VERSION"
    printf '%s\n' '1.2.3' > "$CASE/etc/kvm/netbird.version"
    printf '%s\n' 'netbird' > "$CASE/etc/kvm/vpn"
    # S95 owns many unrelated boot actions. Extract only its helper section
    # so select_vpn can be exercised without executing host init side effects.
    tr -d '\r' < "$REPO_ROOT/kvmapp/system/init.d/S95nanokvm" |
        awk '/^case "\$1" in/{exit} {print}' |
        sed \
            -e "s|/etc/init.d|$CASE/etc/init.d|g" \
            -e "s|/etc/kvm|$CASE/etc/kvm|g" \
            -e "s|/usr/bin/netbird|$CASE/usr/bin/netbird|g" \
            -e "s|/kvmapp/system|$CASE/kvmapp/system|g" > "$CASE/S95nanokvm"
    chmod 755 "$CASE/S95nanokvm"
}

run_s95() {
    PATH="$CASE/bin:$PATH" sh -c '. "$1"; select_vpn' sh "$CASE/S95nanokvm"
}

prepare_s99() {
    mkdir -p "$CASE/kvmapp/system/netbird" "$CASE/etc/kvm" "$CASE/usr/bin"
    printf '%s\n' '1.2.3' > "$CASE/kvmapp/system/netbird/VERSION"
    printf '%s\n' '1.2.3' > "$CASE/etc/kvm/netbird.version"
    tr -d '\r' < "$REPO_ROOT/kvmapp/system/init.d/S99netbird" |
        sed \
        -e "s|/usr/bin/netbird|$CASE/usr/bin/netbird|g" \
        -e "s|/var/run|$CASE/var/run|g" \
        -e "s|/var/log/netbird|$CASE/var/log/netbird|g" \
        -e "s|/dev/net|$CASE/dev/net|g" \
        -e "s|/sbin/modprobe|$CASE/sbin/modprobe|g" \
        -e "s|/lib/modules/tun.ko|$CASE/lib/modules/tun.ko|g" \
        -e "s|/kvmapp/system/netbird/VERSION|$CASE/kvmapp/system/netbird/VERSION|g" \
        -e "s|/etc/kvm/netbird.version|$CASE/etc/kvm/netbird.version|g" > "$CASE/S99netbird"
    chmod 755 "$CASE/S99netbird"
}

prepare_s98() {
    tr -d '\r' < "$REPO_ROOT/kvmapp/system/init.d/S98tailscaled" |
        sed \
        -e 's|DAEMON_PATH="/usr/sbin/$DAEMON"|DAEMON_PATH="__CASE__/usr/sbin/$DAEMON"|' \
        -e "s|__CASE__|$CASE|g" \
        -e "s|/usr/sbin/tailscaled|$CASE/usr/sbin/tailscaled|g" \
        -e "s|/usr/bin/tailscale|$CASE/usr/bin/tailscale|g" \
        -e "s|/var/run|$CASE/var/run|g" \
        -e "s|/var/lib/tailscale|$CASE/var/lib/tailscale|g" \
        -e "s|/etc/sysctl.d|$CASE/etc/sysctl.d|g" \
        -e "s|/var/log/netbird|$CASE/var/log/netbird|g" > "$CASE/S98tailscaled"
    chmod 755 "$CASE/S98tailscaled"
}

test_s95_rejects_executable_netbird_directory() {
    new_case s95-directory
    prepare_s95
    printf '%s\n' 'tailscale boot script' > "$CASE/etc/init.d/S98tailscaled"
    mkdir "$CASE/usr/bin/netbird"
    chmod 755 "$CASE/usr/bin/netbird"

    run_s95 || fail 'S95 must treat an executable directory as an invalid NetBird binary'
    grep -Fqx 'tailscale boot script' "$CASE/etc/init.d/S98tailscaled" || \
        fail 'S95 removed S98 for an invalid NetBird directory'
    [ ! -e "$CASE/etc/init.d/S99netbird" ] || \
        fail 'S95 published S99 for an invalid NetBird directory'
}

test_s95_accepts_symlink_netbird_binary() {
    new_case s95-symlink
    prepare_s95
    printf '%s\n' 'tailscale boot script' > "$CASE/etc/init.d/S98tailscaled"
    make_executable_file "$CASE/usr/bin/netbird.real"
    if ! make_symlink netbird.real "$CASE/usr/bin/netbird"; then
        printf '%s\n' 'SKIP: symlinks are unavailable on this filesystem'
        return
    fi

    run_s95 || fail 'S95 rejected a symlink to an executable NetBird binary'
    [ ! -e "$CASE/etc/init.d/S98tailscaled" ] || \
        fail 'S95 did not select NetBird for a symlinked executable'
    cmp "$CASE/etc/init.d/S99netbird" "$CASE/kvmapp/system/init.d/S99netbird" >/dev/null || \
        fail 'S95 did not publish S99 for a symlinked executable'
}

test_s95_rejects_broken_netbird_link() {
    new_case s95-broken-link
    prepare_s95
    printf '%s\n' 'tailscale boot script' > "$CASE/etc/init.d/S98tailscaled"
    if ! make_symlink does-not-exist "$CASE/usr/bin/netbird"; then
        printf '%s\n' 'SKIP: symlinks are unavailable on this filesystem'
        return
    fi

    run_s95 || fail 'S95 failed while preserving Tailscale for a broken NetBird link'
    grep -Fqx 'tailscale boot script' "$CASE/etc/init.d/S98tailscaled" || \
        fail 'S95 removed S98 for a broken NetBird link'
    [ ! -e "$CASE/etc/init.d/S99netbird" ] || \
        fail 'S95 published S99 for a broken NetBird link'
}

test_s95_refuses_unsafe_s99_destinations() {
    for kind in directory symlink; do
        new_case "s95-s99-$kind"
        prepare_s95
        make_executable_file "$CASE/usr/bin/netbird"
        printf '%s\n' 'tailscale boot script' > "$CASE/etc/init.d/S98tailscaled"
        case "$kind" in
            directory) mkdir "$CASE/etc/init.d/S99netbird" ;;
            symlink)
                printf '%s\n' 'unrelated script' > "$CASE/etc/init.d/not-s99"
                if ! make_symlink not-s99 "$CASE/etc/init.d/S99netbird"; then
                    printf '%s\n' 'SKIP: symlinks are unavailable on this filesystem'
                    continue
                fi
                ;;
        esac

        if run_s95; then
            fail "S95 accepted an unsafe S99 $kind destination"
        fi
        grep -Fqx 'tailscale boot script' "$CASE/etc/init.d/S98tailscaled" || \
            fail "S95 removed S98 for an unsafe S99 $kind destination"
        case "$kind" in
            directory) [ -d "$CASE/etc/init.d/S99netbird" ] || fail 'S95 replaced S99 directory' ;;
            symlink) [ -L "$CASE/etc/init.d/S99netbird" ] || fail 'S95 replaced S99 symlink' ;;
        esac
    done
}

test_s95_restores_s98_when_s99_publish_fails() {
    new_case s95-publish-failure
    prepare_s95
    make_executable_file "$CASE/usr/bin/netbird"
    printf '%s\n' 'tailscale boot script' > "$CASE/etc/init.d/S98tailscaled"

    if FAIL_NETBIRD_PUBLISH=1 PUBLISH_DEST="$CASE/etc/init.d/S99netbird" \
        PATH="$CASE/bin:$PATH" sh -c '. "$1"; select_vpn' sh "$CASE/S95nanokvm"; then
        fail 'S95 succeeded despite a staged S99 publish failure'
    fi
    grep -Fqx 'tailscale boot script' "$CASE/etc/init.d/S98tailscaled" || \
        fail 'S95 did not restore S98 after S99 publish failure'
    [ ! -e "$CASE/etc/init.d/S99netbird" ] || \
        fail 'S95 left an active S99 after publish failure'
}

test_s95_reports_failed_s98_rollback() {
    new_case s95-rollback-failure
    prepare_s95
    make_executable_file "$CASE/usr/bin/netbird"
    printf '%s\n' 'tailscale boot script' > "$CASE/etc/init.d/S98tailscaled"

    if output=$(FAIL_NETBIRD_PUBLISH=1 PUBLISH_DEST="$CASE/etc/init.d/S99netbird" \
        FAIL_TAILSCALE_ROLLBACK=1 ROLLBACK_DEST="$CASE/etc/init.d/S98tailscaled" \
        PATH="$CASE/bin:$PATH" sh -c '. "$1"; select_vpn' sh "$CASE/S95nanokvm" 2>&1); then
        fail 'S95 succeeded despite a failed S99 publish and S98 rollback'
    fi
    printf '%s\n' "$output" | grep -F 'Failed to restore Tailscale boot script' >/dev/null || \
        fail 'S95 did not report a failed S98 rollback'
    if ! find "$CASE/etc/init.d" -name '.S98tailscaled.old.*' -type f -print | grep . >/dev/null; then
        fail 'S95 discarded the only recoverable S98 copy after rollback failure'
    fi
    [ ! -e "$CASE/etc/init.d/S99netbird" ] || \
        fail 'S95 left an active S99 after failed rollback'
}

test_s95_recovers_interrupted_switch_when_netbird_is_unavailable() {
    new_case s95-interrupted-switch
    prepare_s95
    # This pair is the durable state left after S98 was hidden but before the
    # staged S99 could be published.  NetBird is deliberately unavailable.
    printf '%s\n' 'tailscale boot script' > "$CASE/etc/init.d/.S98tailscaled.old.interrupted"
    printf '%s\n' 'staged netbird boot script' > "$CASE/etc/init.d/.S99netbird.new.interrupted"

    run_s95 || fail 'S95 could not recover an interrupted boot-script switch'
    grep -Fqx 'tailscale boot script' "$CASE/etc/init.d/S98tailscaled" || \
        fail 'S95 lost the hidden S98 after an interrupted switch'
    [ ! -e "$CASE/etc/init.d/S99netbird" ] || \
        fail 'S95 left S99 active when NetBird is unavailable'
    if find "$CASE/etc/init.d" -name '.S*' -print | grep . >/dev/null; then
        fail 'S95 left interrupted boot-script transaction state behind'
    fi
}

test_s95_cleans_stale_successful_backup_for_valid_netbird() {
    new_case s95-stale-successful-backup
    prepare_s95
    make_executable_file "$CASE/usr/bin/netbird"
    # No matching staged S99 means publish had completed before power loss;
    # this backup is stale and must not restore Tailscale beside NetBird.
    printf '%s\n' 'obsolete tailscale boot script' > "$CASE/etc/init.d/.S98tailscaled.old.successful"
    printf '%s\n' 'previous netbird boot script' > "$CASE/etc/init.d/S99netbird"
    chmod 755 "$CASE/etc/init.d/S99netbird"

    run_s95 || fail 'S95 could not install a valid NetBird boot script'
    [ ! -e "$CASE/etc/init.d/S98tailscaled" ] || \
        fail 'S95 resurrected stale Tailscale state after a successful NetBird switch'
    cmp "$CASE/etc/init.d/S99netbird" "$CASE/kvmapp/system/init.d/S99netbird" >/dev/null || \
        fail 'S95 did not replace stale S99 for valid NetBird selection'
    if find "$CASE/etc/init.d" -name '.S*' -print | grep . >/dev/null; then
        fail 'S95 did not clean stale successful transaction state'
    fi
}

test_s95_switches_boot_scripts_atomically() {
    new_case s95-success
    prepare_s95
    make_executable_file "$CASE/usr/bin/netbird"
    printf '%s\n' 'tailscale boot script' > "$CASE/etc/init.d/S98tailscaled"

    run_s95 || fail 'S95 could not install a valid NetBird boot script'
    cmp "$CASE/etc/init.d/S99netbird" "$CASE/kvmapp/system/init.d/S99netbird" >/dev/null || \
        fail 'S95 did not publish the expected S99 script'
    [ ! -e "$CASE/etc/init.d/S98tailscaled" ] || \
        fail 'S95 left Tailscale selected after NetBird switch'
    if find "$CASE/etc/init.d" -name '.S*' -print | grep . >/dev/null; then
        fail 'S95 left a temporary boot-script transaction file behind'
    fi
}

test_s99_restart_preflights_symlink_binary() {
    new_case s99-restart-symlink
    prepare_s99
    make_executable_file "$CASE/usr/bin/netbird.real"
    if ! make_symlink netbird.real "$CASE/usr/bin/netbird"; then
        printf '%s\n' 'SKIP: symlinks are unavailable on this filesystem'
        return
    fi
    : > "$CASE/pidof.log"

    # The stub executable exits before daemon readiness, so restart itself
    # fails. A pidof call proves the symlink passed the binary preflight and
    # execution reached the stop path.
    PIDOF_LOG="$CASE/pidof.log" PATH="$CASE/bin:$PATH" "$CASE/S99netbird" restart || true
    [ -s "$CASE/pidof.log" ] || fail 'S99 restart rejected a symlinked executable before stop'
}

test_s99_restart_preflights_regular_binary() {
    new_case s99-restart-directory
    prepare_s99
    mkdir "$CASE/usr/bin/netbird"
    chmod 755 "$CASE/usr/bin/netbird"
    : > "$CASE/pidof.log"

    if PIDOF_LOG="$CASE/pidof.log" PATH="$CASE/bin:$PATH" "$CASE/S99netbird" restart; then
        fail 'S99 restart accepted an executable NetBird directory'
    fi
    [ ! -s "$CASE/pidof.log" ] || fail 'S99 restart stopped/inspected daemon before binary preflight'
}

test_s99_restart_preflights_version_pin() {
    new_case s99-restart-pin
    prepare_s99
    make_executable_file "$CASE/usr/bin/netbird"
    printf '%s\n' '1.2.4' > "$CASE/etc/kvm/netbird.version"
    : > "$CASE/pidof.log"

    if PIDOF_LOG="$CASE/pidof.log" PATH="$CASE/bin:$PATH" "$CASE/S99netbird" restart; then
        fail 'S99 restart accepted a NetBird binary with a mismatched version pin'
    fi
    [ ! -s "$CASE/pidof.log" ] || fail 'S99 restart inspected daemon before version-pin preflight'
}

test_s98_restart_preflights_regular_binaries() {
    new_case s98-restart-directory
    prepare_s98
    mkdir "$CASE/usr/sbin/tailscaled"
    chmod 755 "$CASE/usr/sbin/tailscaled"
    make_executable_file "$CASE/usr/bin/tailscale"
    : > "$CASE/pidof.log"

    if PIDOF_LOG="$CASE/pidof.log" PATH="$CASE/bin:$PATH" "$CASE/S98tailscaled" restart; then
        fail 'S98 restart accepted an executable tailscaled directory'
    fi
    [ ! -s "$CASE/pidof.log" ] || fail 'S98 restart stopped/inspected daemon before binary preflight'
}

test_s98_restart_preflights_client_regular_file() {
    new_case s98-restart-client-directory
    prepare_s98
    make_executable_file "$CASE/usr/sbin/tailscaled"
    mkdir "$CASE/usr/bin/tailscale"
    chmod 755 "$CASE/usr/bin/tailscale"
    : > "$CASE/pidof.log"

    if PIDOF_LOG="$CASE/pidof.log" PATH="$CASE/bin:$PATH" "$CASE/S98tailscaled" restart; then
        fail 'S98 restart accepted an executable tailscale client directory'
    fi
    [ ! -s "$CASE/pidof.log" ] || fail 'S98 restart inspected daemon before client preflight'
}

test_s98_restart_preflights_symlink_binary() {
    new_case s98-restart-symlink
    prepare_s98
    make_executable_file "$CASE/usr/sbin/tailscaled.real"
    if ! make_symlink tailscaled.real "$CASE/usr/sbin/tailscaled"; then
        printf '%s\n' 'SKIP: symlinks are unavailable on this filesystem'
        return
    fi
    make_executable_file "$CASE/usr/bin/tailscale"
    : > "$CASE/pidof.log"

    # The stub daemon exits before readiness; pidof proves that restart passed
    # its symlink preflight and reached the stop path.
    PIDOF_LOG="$CASE/pidof.log" PATH="$CASE/bin:$PATH" "$CASE/S98tailscaled" restart || true
    [ -s "$CASE/pidof.log" ] || fail 'S98 restart rejected a symlinked executable before stop'
}

test_stop_does_not_require_present_binary() {
    new_case stop-without-binary
    prepare_s99
    : > "$CASE/pidof.log"
    PIDOF_LOG="$CASE/pidof.log" PATH="$CASE/bin:$PATH" "$CASE/S99netbird" stop || \
        fail 'S99 stop refused when its binary was already deleted'
    [ -s "$CASE/pidof.log" ] || fail 'S99 stop did not inspect daemon state'

    new_case tailscale-stop-without-binary
    prepare_s98
    : > "$CASE/pidof.log"
    PIDOF_LOG="$CASE/pidof.log" PATH="$CASE/bin:$PATH" "$CASE/S98tailscaled" stop || \
        fail 'S98 stop refused when its binary was already deleted'
    [ -s "$CASE/pidof.log" ] || fail 'S98 stop did not inspect daemon state'
}

test_s95_rejects_executable_netbird_directory
test_s95_accepts_symlink_netbird_binary
test_s95_rejects_broken_netbird_link
test_s95_refuses_unsafe_s99_destinations
test_s95_restores_s98_when_s99_publish_fails
test_s95_reports_failed_s98_rollback
test_s95_recovers_interrupted_switch_when_netbird_is_unavailable
test_s95_cleans_stale_successful_backup_for_valid_netbird
test_s95_switches_boot_scripts_atomically
test_s99_restart_preflights_symlink_binary
test_s99_restart_preflights_regular_binary
test_s99_restart_preflights_version_pin
test_s98_restart_preflights_regular_binaries
test_s98_restart_preflights_client_regular_file
test_s98_restart_preflights_symlink_binary
test_stop_does_not_require_present_binary
printf '%s\n' 'VPN init-script regression tests: PASS'

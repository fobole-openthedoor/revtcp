#!/bin/bash
# Visible, system-wide launchd service, running as a non-root local user.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin

APP='/Library/Application Support/revtcp'
LOG_DIR='/Library/Logs/revtcp'
PLIST='/Library/LaunchDaemons/local.revtcp.client.plist'
SERVICE='system/local.revtcp.client'
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
stage_dir=''

fail() { printf 'revtcp-service: %s\n' "$*" >&2; exit 1; }
cleanup() { if [[ -n "$stage_dir" ]]; then rm -rf "$stage_dir"; fi; }
trap cleanup EXIT

usage() {
    cat <<'USAGE'
Usage (on macOS):
  sudo ./service.sh install --binary ./revtcp --config ./client.json --cert-dir ./certs [--user LOCAL_USER]
  sudo ./service.sh check|start|stop|restart|status|logs
  sudo ./service.sh upgrade --binary ./revtcp
  sudo ./service.sh uninstall [--purge]

install defaults to the ordinary user who invoked sudo. It starts the client
immediately and enables boot-time launch. Existing installations are not replaced.
stop persists across reboots; start enables boot-time launch again.
uninstall retains configuration, binaries and logs; --purge deletes those too.
USAGE
}

[[ $# -gt 0 ]] || { usage; exit 0; }
action=$1
shift
case "$action" in help|-h|--help) usage; exit 0;; esac
[[ $(uname -s) == Darwin ]] || fail 'this service manager must run on macOS'
[[ $(id -u) == 0 ]] || fail 'run this command with sudo to manage the system LaunchDaemon'

run_user() {
    [[ -f "$PLIST" ]] || fail 'service is not installed'
    /usr/libexec/PlistBuddy -c 'Print :UserName' "$PLIST"
}

check_config() {
    local user
    user=$(run_user)
    sudo -u "$user" "$APP/bin/revtcp" client --config "$APP/config/client.json" \
        --log-file "$LOG_DIR/client.log" --check-config
}

bootout_if_loaded() {
    if launchctl print "$SERVICE" >/dev/null 2>&1; then
        launchctl bootout "$SERVICE"
    fi
}

start_service() {
    check_config
    launchctl enable "$SERVICE"
    if ! launchctl print "$SERVICE" >/dev/null 2>&1; then
        launchctl bootstrap system "$PLIST"
    fi
}

install_service() {
    local binary='' config='' cert_dir='' user=${SUDO_USER:-} group file
    while [[ $# -gt 0 ]]; do
        [[ $# -ge 2 ]] || fail "missing value for $1"
        case "$1" in
            --binary) binary=$2;;
            --config) config=$2;;
            --cert-dir) cert_dir=$2;;
            --user) user=$2;;
            *) fail "unknown install option: $1";;
        esac
        shift 2
    done
    [[ -n "$binary" && -n "$config" && -n "$cert_dir" ]] || fail '--binary, --config and --cert-dir are required'
    [[ "$user" =~ ^[a-zA-Z_][a-zA-Z0-9_.-]*$ ]] || fail 'specify a local short username using --user'
    [[ $(id -u "$user") != 0 ]] || fail 'choose a non-root local user with --user'
    group=$(id -g "$user")
    [[ ! -e "$APP" && ! -L "$APP" && ! -e "$PLIST" && ! -L "$PLIST" ]] || fail 'existing installation/data found; use upgrade, edit config, or back it up before uninstall --purge'
    [[ -f "$binary" && -f "$config" ]] || fail 'binary or configuration file is missing'
    for file in ca.crt client.crt client.key; do
        [[ -f "$cert_dir/$file" ]] || fail "missing certificate file: $cert_dir/$file"
    done
    [[ -f "$SCRIPT_DIR/local.revtcp.client.plist" ]] || fail 'launchd template is missing beside service.sh'

    # Validate the staged program/config/certificates as the actual service user,
    # before installing anything in /Library or changing launchd state.
    stage_dir=$(mktemp -d /private/tmp/revtcp-install.XXXXXX)
    chown "$user:$group" "$stage_dir"
    install -d -m 700 -o "$user" -g "$group" "$stage_dir/config" "$stage_dir/config/certs"
    install -m 755 "$binary" "$stage_dir/revtcp"
    install -m 600 -o "$user" -g "$group" "$config" "$stage_dir/config/client.json"
    for file in ca.crt client.crt client.key; do
        install -m 600 -o "$user" -g "$group" "$cert_dir/$file" "$stage_dir/config/certs/$file"
    done
    sudo -u "$user" "$stage_dir/revtcp" client --config "$stage_dir/config/client.json" \
        --log-file "$stage_dir/check.log" --check-config
    sed "s/__RUN_USER__/$user/" "$SCRIPT_DIR/local.revtcp.client.plist" > "$stage_dir/service.plist"
    plutil -lint "$stage_dir/service.plist"

    install -d -m 755 -o root -g wheel "$APP" "$APP/bin"
    install -d -m 700 -o "$user" -g "$group" "$APP/config" "$APP/config/certs" "$LOG_DIR"
    install -m 755 -o root -g wheel "$stage_dir/revtcp" "$APP/bin/revtcp"
    install -m 755 -o root -g wheel "$SCRIPT_DIR/service.sh" "$APP/service.sh"
    install -m 600 -o "$user" -g "$group" "$stage_dir/config/client.json" "$APP/config/client.json"
    for file in ca.crt client.crt client.key; do
        install -m 600 -o "$user" -g "$group" "$stage_dir/config/certs/$file" "$APP/config/certs/$file"
    done
    install -m 600 -o "$user" -g "$group" /dev/null "$LOG_DIR/launchd.log"
    install -m 644 -o root -g wheel "$stage_dir/service.plist" "$PLIST"
    start_service
    printf 'Installed and registered %s as %s. Check status and logs to confirm connectivity.\n' "$SERVICE" "$user"
}

upgrade_service() {
    [[ $# == 2 && "$1" == --binary ]] || fail 'usage: upgrade --binary PATH'
    [[ -f "$2" ]] || fail 'new binary does not exist'
    local user
    user=$(run_user)
    stage_dir=$(mktemp -d /private/tmp/revtcp-upgrade.XXXXXX)
    chown "$user" "$stage_dir"
    install -m 755 "$2" "$stage_dir/revtcp"
    sudo -u "$user" "$stage_dir/revtcp" client --config "$APP/config/client.json" \
        --log-file "$LOG_DIR/client.log" --check-config
    install -m 755 -o root -g wheel "$stage_dir/revtcp" "$APP/bin/revtcp.new"
    install -m 755 -o root -g wheel "$APP/bin/revtcp" "$APP/bin/revtcp.previous"
    bootout_if_loaded
    mv -f "$APP/bin/revtcp.new" "$APP/bin/revtcp"
    start_service
    printf 'Upgraded. Previous executable: %s/bin/revtcp.previous\n' "$APP"
}

case "$action" in
    install) install_service "$@";;
    upgrade) upgrade_service "$@";;
    uninstall)
        purge=false
        if [[ $# -gt 0 ]]; then
            [[ $# == 1 && "$1" == --purge ]] || fail 'usage: uninstall [--purge]'
            purge=true
        fi
        launchctl disable "$SERVICE"
        bootout_if_loaded
        rm -f "$PLIST"
        if [[ "$purge" == true ]]; then
            rm -rf "$APP" "$LOG_DIR"
            printf 'Service and its installed files removed.\n'
        else
            printf 'Autostart uninstalled. Configuration, binaries and logs retained in:\n%s\n%s\n' "$APP" "$LOG_DIR"
        fi
        ;;
    check|start|stop|restart|status|logs)
        [[ $# == 0 ]] || fail "$action takes no arguments"
        case "$action" in
            check) check_config;;
            start) start_service;;
            stop) launchctl disable "$SERVICE"; bootout_if_loaded;;
            restart) check_config; bootout_if_loaded; start_service;;
            status) launchctl print "$SERVICE";;
            logs) tail -n 100 -F "$LOG_DIR/client.log" "$LOG_DIR/launchd.log";;
        esac
        ;;
    *) usage; fail "unknown action: $action";;
esac

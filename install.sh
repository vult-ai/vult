#!/bin/sh
# Vult 0.1.0 — https://vult.sh

vult_install() {
    set -eu
    if [ "$#" -ne 0 ]; then
        echo 'Vult: This installer does not take arguments.' >&2
        return 1
    fi
    if [ "$(/usr/bin/uname -s)" != Darwin ]; then
        echo 'Vult: This release requires macOS.' >&2
        return 1
    fi
    case "$(/usr/bin/uname -m)" in
        arm64) ;;
        *) echo 'Vult: This release does not support this processor.' >&2; return 1 ;;
    esac
    major=$(/usr/bin/sw_vers -productVersion | /usr/bin/cut -d. -f1)
    case "$major" in
        ''|*[!0-9]*) echo 'Vult: Could not determine the macOS version.' >&2; return 1 ;;
    esac
    if [ "$major" -lt 26 ]; then
        echo 'Vult: This release requires macOS 26 or later.' >&2
        return 1
    fi
    if [ "$(/usr/bin/id -u)" -eq 0 ]; then
        echo 'Vult: Run this command as yourself. macOS will ask for administrator approval.' >&2
        return 1
    fi
    /usr/bin/printf '%s\n' 'Installing Vult 0.1.0. macOS will ask for administrator approval.'
    /usr/bin/osascript <<'VULT_ADMIN'
set installLines to {¬
    "set -eu", ¬
    "umask 077", ¬
    "[ \"$(/usr/bin/id -u)\" -eq 0 ] || exit 1", ¬
    "stage=$(/usr/bin/mktemp -d /private/var/tmp/vult-install.XXXXXXXX)", ¬
    "trap '/bin/rm -rf -- \"$stage\"' EXIT", ¬
    "trap 'exit 129' HUP", ¬
    "trap 'exit 130' INT", ¬
    "trap 'exit 143' TERM", ¬
    "if ! /usr/bin/curl -q --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --max-redirs 5 --connect-timeout 15 --max-time 180 --max-filesize 134217728 --output \"$stage/vult.pkg\" 'https://github.com/vult-ai/vult/releases/download/v0.1.0/vult-0.1.0-macos-arm64.pkg'; then", ¬
    "    echo 'Vult: Could not download the release. Nothing was installed.' >&2; exit 1", ¬
    "fi", ¬
    "if ! /usr/bin/printf '%s  %s\\n' '28b70ff7ed1d664b6414c64f0b6c4e05a38a8a43c944f5aa645b9ec0092bd862' \"$stage/vult.pkg\" | /usr/bin/shasum -a 256 -c -; then", ¬
    "    echo 'Vult: Release checksum mismatch. Nothing was installed.' >&2; exit 1", ¬
    "fi", ¬
    "if ! /usr/sbin/installer -pkg \"$stage/vult.pkg\" -target /; then", ¬
    "    echo 'Vult: Installation did not complete. Check the system install log before trying again.' >&2; exit 1", ¬
    "fi", ¬
    "echo 'Run /Library/PrivilegedHelperTools/ai.vult.ledger/vult init from your own terminal.'"}
set previousDelimiters to AppleScript's text item delimiters
set AppleScript's text item delimiters to linefeed
set installScript to installLines as text
set AppleScript's text item delimiters to previousDelimiters
set installCommand to "/usr/bin/env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin LANG=C LC_ALL=C /bin/sh -c " & quoted form of installScript
do shell script installCommand with administrator privileges with prompt "Install Vult 0.1.0 and its protected local ledger."
VULT_ADMIN
}

# Keep the entry point last: an incomplete download must not start installation.
vult_install "$@"

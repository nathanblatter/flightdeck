#!/usr/bin/env bash
# Deploy hook for the flightdeck mailbox.
#
# This is the ONLY thing the CI deploy key is permitted to run. Its
# authorized_keys entry pins it with command="/usr/local/sbin/mailbox-deploy"
# plus restrict, so a stolen key cannot open a shell, forward a port, or run
# anything else on the host — it can only hand over a new binary.
#
# The new binary arrives on stdin. It is staged, smoke-tested, and only then
# swapped into place, so a broken build fails before it can take the service
# down rather than after.
set -euo pipefail

BIN=/usr/local/bin/flightdeck-mailbox
STAGE=$(mktemp /tmp/mailbox-deploy.XXXXXX)
trap 'rm -f "$STAGE"' EXIT

cat > "$STAGE"
chmod 0755 "$STAGE"

if [ ! -s "$STAGE" ]; then
    echo "deploy: empty upload, refusing" >&2
    exit 1
fi

# Refuse anything that isn't a Linux x86-64 executable — a wrong-arch or
# truncated upload would otherwise only surface as a crashloop after the swap.
# Read the ELF header directly rather than shelling out to file(1), which is
# not installed on a minimal server and whose absence must not be mistaken for
# a bad binary.
header=$(od -An -tx1 -N20 "$STAGE" | tr -d ' \n')
magic=${header:0:8}     # 7f 45 4c 46  — "\x7fELF"
elfclass=${header:8:2}  # 02 = 64-bit
machine=${header:36:4}  # e_machine at offset 18, little-endian: 3e00 = x86-64
if [ "$magic" != "7f454c46" ] || [ "$elfclass" != "02" ] || [ "$machine" != "3e00" ]; then
    echo "deploy: not a linux/amd64 ELF binary (magic=$magic class=$elfclass machine=$machine)" >&2
    exit 1
fi

# The binary must at least be able to run and report its version.
if ! VERSION=$("$STAGE" version 2>&1); then
    echo "deploy: staged binary failed to execute: $VERSION" >&2
    exit 1
fi

echo "deploy: staged flightdeck-mailbox $VERSION"

# Keep the outgoing binary so a bad deploy can be rolled back by hand without
# waiting on CI.
if [ -f "$BIN" ]; then
    cp -f "$BIN" "${BIN}.previous"
fi

# Rename is atomic: no window where the path exists but is half-written.
mv -f "$STAGE" "$BIN"
trap - EXIT

systemctl restart flightdeck-mailbox

# Confirm it actually came up rather than reporting success on a crashloop.
for _ in $(seq 1 15); do
    if systemctl is-active --quiet flightdeck-mailbox; then
        sleep 2
        if systemctl is-active --quiet flightdeck-mailbox; then
            echo "deploy: flightdeck-mailbox $VERSION is running"
            exit 0
        fi
    fi
    sleep 1
done

echo "deploy: service did not stay up after restart" >&2
systemctl status flightdeck-mailbox --no-pager --lines=20 >&2 || true
exit 1

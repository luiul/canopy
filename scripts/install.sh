#!/usr/bin/env bash
set -euo pipefail

# Builds canopy and installs it to ~/.local/bin (or $CANOPY_INSTALL_DIR),
# then code-signs it with a stable local certificate + identifier instead
# of leaving it plain ad-hoc-signed.
#
# Why this matters: every unsigned Go binary macOS ad-hoc-signs at link
# time carries the same generic identifier ("a.out"), with the signature
# itself keyed to that exact binary's content hash (cdhash). Rebuilding
# changes the hash, so macOS treats the new binary as a brand-new,
# never-before-seen app and drops any Accessibility/Automation
# permission already granted to the old one. canopy needs both (for
# mycelium's window-detection AppleScript, backing jump's open-or-focus
# behavior) on every run, so without a stable signature it'd need
# re-approving after every single rebuild.
#
# Signing with a real certificate (self-signed is fine for local dev)
# anchors the permission to that certificate + a fixed --identifier
# instead of the binary's hash, so the grant survives rebuilds as long
# as the same cert + identifier are reused. This script assumes that
# certificate already exists in your login keychain (create one once,
# e.g. via openssl + `security import`/`add-trusted-cert`, named
# whatever CANOPY_CODESIGN_CERT below expects) — if it can't find one,
# it still installs, just without a stable signature, and warns that
# Accessibility/Automation will need re-granting after the next
# rebuild.

CERT_NAME="${CANOPY_CODESIGN_CERT:-luiul-local-devtools}"
IDENTIFIER="com.luiul.canopy"
DEST="${CANOPY_INSTALL_DIR:-$HOME/.local/bin}/canopy"

cd "$(dirname "$0")/.."

tmp=$(mktemp -t canopy-build)
go build -o "$tmp" ./cmd/canopy
install -m 0755 "$tmp" "$DEST"
rm -f "$tmp"

if security find-identity -v -p codesigning 2>/dev/null | grep -q "\"$CERT_NAME\""; then
	codesign --force --sign "$CERT_NAME" --identifier "$IDENTIFIER" "$DEST"
	echo "Installed and signed $DEST as $IDENTIFIER."
else
	echo "warning: no codesigning identity named \"$CERT_NAME\" found in the login keychain." >&2
	echo "warning: $DEST is only ad-hoc signed; macOS will likely need Accessibility/Automation" >&2
	echo "warning: permission re-granted after every future rebuild. See scripts/install.sh's" >&2
	echo "warning: own comment for why, and how to set up a stable local signing identity." >&2
	echo "Installed (unsigned) $DEST." >&2
fi

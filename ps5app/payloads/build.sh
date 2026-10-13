#!/bin/bash
# Builds Loadout's PKG installer payload with PS5 React's payload SDK, inside WSL:
#   wsl -d Ubuntu-24.04 -u root -- bash "/mnt/g/Vibe Coded Apps/Loadout/ps5app/payloads/build.sh"
# Loadout carries the result (pkg-install.elf) and sends it to the PS5's payload loader.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
PS5_REACT="${PS5_REACT:-/root/ps5-react}"
SDK="$PS5_REACT/.deps/sdk/ps5-payload-sdk"
PS5_PAYLOAD_SDK="$SDK" "$SDK/bin/prospero-clang" -std=gnu11 -O2 -Wall -Wextra -Werror \
  -o "$HERE/pkg-install.elf" "$HERE/pkg_install.c" -lkernel_sys -lSceUserService
echo "Built $HERE/pkg-install.elf"

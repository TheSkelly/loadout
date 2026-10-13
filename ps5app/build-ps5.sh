#!/bin/bash
# Builds the Loadout PS5 app with PS5 React, inside WSL (Ubuntu).
#   wsl -d Ubuntu-24.04 -u root -- bash "/mnt/g/Vibe Coded Apps/Loadout/ps5app/build-ps5.sh"
# Needs PS5 React checked out at $PS5_REACT (default /root/ps5-react) with
# its setup done (npm ci, .venv with requirements.txt). The finished app
# folder lands in ps5app/dist/<titleId>, which Loadout's own build packs in.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
PS5_REACT="${PS5_REACT:-/root/ps5-react}"
TITLE=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['titleId'])" "$HERE/app.json")
APP="$PS5_REACT/apps/loadout"

# the app's sources go into PS5 React's apps folder, where its build expects them
rm -rf "$APP"
mkdir -p "$APP/assets" "$APP/sce_sys"
cp "$HERE"/app.json "$HERE"/*.jsx "$HERE"/*.js "$APP/"
[ -f "$HERE/sce_sys/icon0.png" ] || (cd "$HERE" && "$PS5_REACT/.venv/bin/python3" tools/make_art.py)
cp "$HERE"/sce_sys/*.png "$APP/sce_sys/"
cp "$PS5_REACT/apps/starter/assets/Inter-Regular.ttf" "$APP/assets/"

cd "$PS5_REACT"
. .venv/bin/activate
npm run build -- --app loadout

# PS5 React makes every app a game; Loadout belongs in Media
python3 - "$PS5_REACT/dist/$TITLE/sce_sys/param.json" <<'PY'
import json, sys
p = sys.argv[1]
param = json.load(open(p))
param["applicationCategoryType"] = 65536  # native media app
param["contentBadgeType"] = 2
param.pop("gameIntent", None)
json.dump(param, open(p, "w"), indent=2)
open(p, "a").write("\n")
PY

rm -rf "$HERE/dist/$TITLE"
mkdir -p "$HERE/dist"
cp -r "$PS5_REACT/dist/$TITLE" "$HERE/dist/$TITLE"
echo "Built $HERE/dist/$TITLE"

#!/usr/bin/env bash
# loc.sh — count lines of code in the niq repo.
#
# Splits counts into:
#   - test vs non-test files
#   - webui (internal/webui/assets/src) counted separately
#
# Only git-tracked files are counted, so build artifacts, node_modules,
# the compiled `niq` binary and the webui `dist/` bundles are excluded.
#
# Portable: avoids bash associative arrays (works on macOS /bin/bash 3.2).

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# Code file extensions we treat as "source".
CODE_EXT='go|ts|tsx|js|jsx|css|html|py|sh|rs'

# Webui source root (the actual TS/React sources, not the built dist/).
WEBUI_SRC='internal/webui/assets/src'

is_test() {
  # Go convention: *_test.go
  case "$1" in *_test.go) return 0 ;; esac
  # JS/TS convention: *.test.* or *.spec.*
  if [[ "$1" =~ \.(test|spec)\.[tj]sx?$ ]]; then return 0; fi
  return 1
}

# counters
go_nt_f=0; go_nt_l=0
go_t_f=0;  go_t_l=0
wu_nt_f=0; wu_nt_l=0
wu_t_f=0;  wu_t_l=0
other_f=0; other_l=0
total_f=0; total_l=0

while IFS= read -r f; do
  [[ "$f" =~ \.($CODE_EXT)$ ]] || continue
  [[ "$f" == internal/webui/assets/dist/* ]] && continue

  n=$(wc -l < "$f" | tr -d ' ')
  total_f=$((total_f + 1))
  total_l=$((total_l + n))

  if is_test "$f"; then t=1; else t=0; fi
  if [[ "$f" == "$WEBUI_SRC"/* ]]; then wu=1; else wu=0; fi

  case "$wu:$t" in
    1:0) wu_nt_f=$((wu_nt_f+1)); wu_nt_l=$((wu_nt_l+n)) ;;
    1:1) wu_t_f=$((wu_t_f+1));   wu_t_l=$((wu_t_l+n)) ;;
    0:0) go_nt_f=$((go_nt_f+1)); go_nt_l=$((go_nt_l+n)) ;;
    0:1) go_t_f=$((go_t_f+1));   go_t_l=$((go_t_l+n)) ;;
  esac
done < <(git ls-files)

# "other" = tracked code that is neither Go nor webui source
other_f=$((total_f - go_nt_f - go_t_f - wu_nt_f - wu_t_f))
other_l=$((total_l - go_nt_l - go_t_l - wu_nt_l - wu_t_l))

non_test_f=$((go_nt_f + wu_nt_f + other_f))
non_test_l=$((go_nt_l + wu_nt_l + other_l))
test_f=$((go_t_f + wu_t_f))
test_l=$((go_t_l + wu_t_l))
wu_f=$((wu_nt_f + wu_t_f))
wu_l=$((wu_nt_l + wu_t_l))

fmt() { printf '%-22s %6s %10s\n' "$1" "$2" "$3"; }

echo "niq lines-of-code report (git-tracked source only)"
echo
fmt "Category" "Files" "Lines"
echo "------------------------------------------------------"
fmt "Go non-test"      "$go_nt_f" "$go_nt_l"
fmt "Go test"          "$go_t_f"  "$go_t_l"
fmt "WebUI non-test"   "$wu_nt_f" "$wu_nt_l"
fmt "WebUI test"       "$wu_t_f"  "$wu_t_l"
fmt "Other code"       "$other_f" "$other_l"
echo "------------------------------------------------------"
fmt "TOTAL (all code)" "$total_f" "$total_l"
fmt "  - non-test"     "$non_test_f" "$non_test_l"
fmt "  - test"         "$test_f"     "$test_l"
fmt "  - webui (src)"  "$wu_f"       "$wu_l"

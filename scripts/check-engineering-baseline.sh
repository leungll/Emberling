#!/bin/sh
# Compare protected migration history and file allowances with a trusted Git commit.
set -eu
mode=${1:?usage: check-engineering-baseline.sh migrations|file-size}
if [ "${CI:-}" = true ] && [ -z "${EMBERLING_CHECK_BASE:-}" ]; then
  echo 'CI must set EMBERLING_CHECK_BASE to the comparison commit.' >&2
  exit 1
fi
base=${EMBERLING_CHECK_BASE:-HEAD}
if ! base=$(git rev-parse --verify "$base^{commit}" 2>/dev/null); then
  echo 'EMBERLING_CHECK_BASE does not resolve to an available commit.' >&2
  exit 1
fi
tmp=$(mktemp -d "${TMPDIR:-/tmp}/emberling-baseline.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
case "$mode" in
migrations)
  manifest=backend/migrations/checksums.sha256
  if git cat-file -e "$base:$manifest" 2>/dev/null; then
    git show "$base:$manifest" > "$tmp/old"
    [ -f "$manifest" ] || { echo "Historical manifest missing: $manifest" >&2; exit 1; }
    head -n "$(wc -l < "$tmp/old" | tr -d ' ')" "$manifest" > "$tmp/current"
    if ! cmp -s "$tmp/old" "$tmp/current"; then
      echo 'Historical migration checksum lines must remain unchanged; only append new lines.' >&2
      exit 1
    fi
  fi
  git ls-tree -r --name-only "$base" -- backend/migrations > "$tmp/paths"
  while IFS= read -r path; do
    case "$path" in *.sql) ;; *) continue ;; esac
    git show "$base:$path" > "$tmp/old"
    if [ ! -f "$path" ] || ! cmp -s "$tmp/old" "$path"; then
      echo "Historical migration changed or missing: $path" >&2
      exit 1
    fi
  done < "$tmp/paths"
  ;;
file-size)
  baseline=scripts/file-size-baseline.txt
  : > "$tmp/old"
  : > "$tmp/current"
  if git cat-file -e "$base:$baseline" 2>/dev/null; then
    git show "$base:$baseline" > "$tmp/old"
  fi
  if [ -f "$baseline" ]; then cp "$baseline" "$tmp/current"; fi
  for file in "$tmp/old" "$tmp/current"; do
    awk '
      /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
      NF != 2 || $1 !~ /^[0-9]+$/ || $1 < 1 || seen[$2]++ {
        print "Invalid or duplicate file allowance: " $0 > "/dev/stderr"; failed=1
      }
      END { exit failed }
    ' "$file"
  done
  awk '
    FILENAME == ARGV[1] {
      if (NF == 2 && $1 ~ /^[0-9]+$/) old[$2]=$1
      next
    }
    NF == 2 && $1 ~ /^[0-9]+$/ {
      if (!($2 in old) || $1 > old[$2]) {
        print "File allowance added or raised: " $2 > "/dev/stderr"; failed=1
      }
    }
    END { exit failed }
  ' "$tmp/old" "$tmp/current"
  ;;
*) echo "Unknown baseline check: $mode" >&2; exit 2 ;;
esac

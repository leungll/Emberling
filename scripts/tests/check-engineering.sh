#!/bin/sh
# Exercise repository guards in an isolated Git repository, including bypass attempts.
set -eu
source_dir=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d "${TMPDIR:-/tmp}/emberling-guards.XXXXXX")
trap 'rm -rf "$fixture"' EXIT HUP INT TERM
mkdir -p "$fixture/scripts" "$fixture/backend/migrations" "$fixture/backend/internal"
cp "$source_dir"/*.sh "$fixture/scripts/"
cd "$fixture"
# User signing, templates and hooks must not affect isolated test commits.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
git init -q
git config user.email guards@example.invalid
git config user.name 'Guard tests'
printf 'SELECT 1;\n' > backend/migrations/00001_initial.sql
hash_manifest() {
  if command -v sha256sum >/dev/null 2>&1; then
    (cd backend/migrations && sha256sum 00001_initial.sql) > backend/migrations/checksums.sha256
  else
    (cd backend/migrations && shasum -a 256 00001_initial.sql) > backend/migrations/checksums.sha256
  fi
}
hash_manifest
awk 'BEGIN { for (i=0; i<1202; i++) print "// source" }' > backend/internal/large.go
printf '1202 backend/internal/large.go\n' > scripts/file-size-baseline.txt
git add .
git commit -qm baseline
base=$(git rev-parse HEAD)
export EMBERLING_CHECK_BASE=$base
passed=0
expect() {
  outcome=$1
  label=$2
  shift 2
  if "$@" > "$fixture/output" 2>&1; then actual=pass; else actual=fail; fi
  if [ "$actual" != "$outcome" ]; then
    printf 'FAIL %s: expected %s, got %s\n' "$label" "$outcome" "$actual" >&2
    cat "$fixture/output" >&2
    exit 1
  fi
  passed=$((passed+1))
}
restore() { git reset --hard -q "$base"; git clean -fdq; }
expect pass 'unchanged migrations' sh scripts/check-migrations.sh check
printf 'SELECT 2;\n' > backend/migrations/00001_initial.sql
hash_manifest
expect fail 'rewriting migration and its checksum together' sh scripts/check-migrations.sh check
expect fail 'register cannot hide rewritten history' sh scripts/check-migrations.sh register
# A committed bypass must also be rejected when CI supplies the earlier commit.
git add backend/migrations
git commit -qm rewritten
expect fail 'committed rewrite against explicit base' sh scripts/check-migrations.sh check
restore
printf 'SELECT 2;\n' > backend/migrations/00002_next.sql
expect pass 'registering a forward migration' sh scripts/check-migrations.sh register
expect pass 'checking appended migration' sh scripts/check-migrations.sh check
restore
rm backend/migrations/00001_initial.sql
: > backend/migrations/checksums.sha256
expect fail 'deleting historical migration and checksum' sh scripts/check-migrations.sh check
restore
expect pass 'unchanged file allowance' sh scripts/check-file-size.sh
printf '1203 backend/internal/large.go\n' > scripts/file-size-baseline.txt
expect fail 'raising allowance' sh scripts/check-file-size.sh
restore
printf '1201 backend/internal/large.go\n' > scripts/file-size-baseline.txt
sed '$d' backend/internal/large.go > shorter
mv shorter backend/internal/large.go
expect pass 'lowering allowance with source' sh scripts/check-file-size.sh
restore
printf '1202 backend/internal/new.go\n' >> scripts/file-size-baseline.txt
cp backend/internal/large.go backend/internal/new.go
expect fail 'adding an allowance' sh scripts/check-file-size.sh
restore
sed '1,2d' backend/internal/large.go > shorter
mv shorter backend/internal/large.go
: > scripts/file-size-baseline.txt
expect pass 'removing allowance after splitting' sh scripts/check-file-size.sh
restore
cp backend/internal/large.go backend/internal/new.go
expect fail 'untracked oversized source' sh scripts/check-file-size.sh
restore
printf 'bad backend/internal/large.go\n' > scripts/file-size-baseline.txt
expect fail 'malformed allowance' sh scripts/check-file-size.sh
restore
expect fail 'missing baseline commit fails closed' env EMBERLING_CHECK_BASE=missing-commit sh scripts/check-migrations.sh check
expect fail 'CI must supply a base' env CI=true EMBERLING_CHECK_BASE= sh scripts/check-file-size.sh
printf 'PASS %s engineering guard cases\n' "$passed"

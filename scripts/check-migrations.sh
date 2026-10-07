#!/bin/sh
# Verifies that shared migrations are never rewritten.
#
# backend/migrations/checksums.sha256 records the SHA-256 of every migration in the format
# `sha256sum -c` and `shasum -a 256 -c` read, with paths relative to backend/migrations.
# Once a migration is listed, its content is frozen: a schema change is a new, forward
# migration. Lines are only ever appended, never rewritten.
#
# Usage:
#   scripts/check-migrations.sh check     fail if a listed migration changed, a migration
#                                         is unlisted, or a listed migration is missing
#   scripts/check-migrations.sh register  append checksums for unlisted migrations only
set -eu

dir=backend/migrations
manifest=$dir/checksums.sha256

if command -v sha256sum >/dev/null 2>&1; then
	hash_file() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	echo "Neither sha256sum nor shasum is available." >&2
	exit 2
fi

listed() {
	[ -f "$manifest" ] || return 1
	awk -v f="$1" '$2 == f { found = 1 } END { exit !found }' "$manifest"
}

mode=${1:-check}
case "$mode" in
register)
	touch "$manifest"
	for path in "$dir"/*.sql; do
		[ -e "$path" ] || continue
		name=${path##*/}
		if ! listed "$name"; then
			printf '%s  %s\n' "$(hash_file "$path")" "$name" >>"$manifest"
			echo "registered $name"
		fi
	done
	;;
check)
	status=0
	if [ ! -f "$manifest" ]; then
		echo "missing $manifest"
		status=1
	else
		while read -r want name; do
			[ -n "$name" ] || continue
			path=$dir/$name
			if [ ! -f "$path" ]; then
				echo "listed migration is missing: $path"
				status=1
			elif [ "$(hash_file "$path")" != "$want" ]; then
				echo "shared migration changed: $path"
				status=1
			fi
		done <"$manifest"
	fi
	for path in "$dir"/*.sql; do
		[ -e "$path" ] || continue
		name=${path##*/}
		if ! listed "$name"; then
			echo "migration is not registered: $path"
			status=1
		fi
	done
	if [ "$status" -ne 0 ]; then
		echo "Shared migrations are immutable: restore the changed file and add a forward"
		echo "migration instead. Register a new migration by appending its checksum with"
		echo "'make migrations-checksum'; never rewrite an existing line of $manifest."
		exit 1
	fi
	;;
*)
	echo "usage: $0 check|register" >&2
	exit 2
	;;
esac

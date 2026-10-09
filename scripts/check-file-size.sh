#!/bin/sh
# Enforces the source file size limit with a shrink-only baseline.
#
# Scope: tracked and untracked, non-test, non-generated source files: backend Go files and Studio
# src TypeScript files. A file over LIMIT lines fails unless scripts/file-size-baseline.txt
# lists it as `<max_lines> <path>`; a listed file fails if it grows past its recorded
# count, and its entry must be removed once the file is back under the limit. Files over
# WARN lines are reported without failing.
set -eu

sh scripts/check-engineering-baseline.sh file-size

LIMIT=1200
WARN=800
baseline=scripts/file-size-baseline.txt

files=$(
	{
		git ls-files --cached --others --exclude-standard -- 'backend/*.go' | grep -v '_test\.go$' || true
		git ls-files --cached --others --exclude-standard -- 'studio/src/*.ts' 'studio/src/*.tsx' |
			grep -vE '\.(test|spec)\.tsx?$|\.gen\.tsx?$|\.d\.ts$' || true
	} | sort -u
)

baseline_max() {
	[ -f "$baseline" ] || return 0
	awk -v f="$1" '$1 !~ /^#/ && $2 == f { print $1; exit }' "$baseline"
}

status=0
for path in $files; do
	[ -f "$path" ] || continue
	case "$path" in
	*.go)
		if head -n 5 "$path" | grep -q '^// Code generated .* DO NOT EDIT\.$'; then
			continue
		fi
		;;
	esac
	lines=$(wc -l <"$path" | tr -d ' ')
	max=$(baseline_max "$path")
	if [ -n "$max" ]; then
		if [ "$lines" -gt "$max" ]; then
			echo "FAIL $path: $lines lines, baseline allows at most $max. Baselined files may only shrink; split it by use case."
			status=1
		elif [ "$lines" -le "$LIMIT" ]; then
			echo "FAIL $path: $lines lines is within the $LIMIT-line limit; remove its entry from $baseline."
			status=1
		elif [ "$lines" -lt "$max" ]; then
			echo "note $path: $lines lines, below its baseline of $max; lower the entry in $baseline to $lines."
		fi
	elif [ "$lines" -gt "$LIMIT" ]; then
		echo "FAIL $path: $lines lines exceeds the $LIMIT-line limit. Split it by use case before adding code."
		status=1
	elif [ "$lines" -gt "$WARN" ]; then
		echo "warn $path: $lines lines; over $WARN, check whether new code belongs here."
	fi
done

if [ -f "$baseline" ]; then
	stale=$(awk '$1 !~ /^#/ && NF >= 2 { print $2 }' "$baseline")
	for path in $stale; do
		if ! printf '%s\n' "$files" | grep -qxF "$path"; then
			echo "FAIL $path: listed in $baseline but no longer a checked source file; remove its entry."
			status=1
		fi
	done
fi

if [ "$status" -ne 0 ]; then
	echo "File size check failed. $baseline lists '<max_lines> <path>' for files allowed over"
	echo "$LIMIT lines; entries may only be lowered or removed, never added or raised."
	exit 1
fi

#!/bin/sh
# Runs each named make target in order as its own make invocation, stops at the first
# failing target with its exit status, and prints the elapsed seconds of every phase that
# ran together with the total.
set -u
make_cmd=${MAKE:-make}
summary=""
total_start=$(date +%s)

report() {
	echo "==> phase timing (seconds)"
	printf '%s' "$summary"
	printf '    %-20s %5s\n' total "$(($(date +%s) - total_start))"
}

for phase in "$@"; do
	phase_start=$(date +%s)
	status=0
	"$make_cmd" --no-print-directory "$phase" || status=$?
	summary="$summary$(printf '    %-20s %5s' "$phase" "$(($(date +%s) - phase_start))")
"
	if [ "$status" -ne 0 ]; then
		report
		echo "phase $phase failed with exit status $status"
		exit "$status"
	fi
done
report

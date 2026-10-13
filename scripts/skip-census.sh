#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
# SPDX-License-Identifier: MIT
#
# skip-census.sh — the closing suite run's skip census (operator rule,
# 2026-10-13: a refactor must never turn a test silently off).
#
# Runs the whole Go suite with the database tier (AVELOXIS_TEST_DB must be
# set) and lists every test that SKIPPED. Any skipped test that is not in
# scripts/skip_census_baseline.txt fails the census: either it is a new
# environment gate (review it, then add it to the baseline) or a test that
# stopped running (fix it). Baseline entries that now run are reported so the
# baseline can shrink. The suite's own failures fail the census too.
#
# TestNoStructuralSkips (scripts/) guards the source side: a skip may only name
# an environment reason. This census guards the run side: what actually skipped.
set -u -o pipefail

if [ -z "${AVELOXIS_TEST_DB:-}" ]; then
	echo "skip-census: set AVELOXIS_TEST_DB (the census is defined for the database-tier run)" >&2
	exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASELINE="$ROOT/scripts/skip_census_baseline.txt"
OUT="$(mktemp -d)"
keep_out=0
trap '[ "$keep_out" -eq 1 ] || rm -rf "$OUT"' EXIT

cd "$ROOT" || exit 2
# SKIP_CENSUS_GOFLAGS: extra go test flags (CI passes -race -shuffle=on).
# shellcheck disable=SC2086
go test ${SKIP_CENSUS_GOFLAGS:-} ./... -count=1 -json > "$OUT/suite.json" 2> "$OUT/stderr.txt"
suite_status=$?

# One line per skipped test: "<package> <Test>" (the module path trimmed).
grep '"Action":"skip"' "$OUT/suite.json" | grep '"Test":"' |
	sed -E 's/.*"Package":"github.com\/aveloxis\/aveloxis\/?([^"]*)".*"Test":"([^"]*)".*/\1 \2/; s/^ /. /' |
	sort -u > "$OUT/skipped.txt"
# The baseline has two sections: the reviewed skips of the reference host
# (reported as "gone" when they run), then, after the ALLOWED marker, skips
# that depend on the host (root, platform, -race timing, tools, server
# privileges) — permitted anywhere, never reported as gone.
awk '/^# --- allowed/{a=1} !/^#/ && NF { print > (a ? "'"$OUT"'/allowed.txt" : "'"$OUT"'/required.txt") }' "$BASELINE"
touch "$OUT/allowed.txt" "$OUT/required.txt"
sort -u -o "$OUT/required.txt" "$OUT/required.txt"
sort -u "$OUT/required.txt" "$OUT/allowed.txt" > "$OUT/baseline.txt"

new="$(comm -23 "$OUT/skipped.txt" "$OUT/baseline.txt")"
gone="$(comm -13 "$OUT/skipped.txt" "$OUT/required.txt")"

echo "skip-census: $(wc -l < "$OUT/skipped.txt" | tr -d ' ') tests skipped (reviewed $(wc -l < "$OUT/required.txt" | tr -d ' '), host-dependent allowed $(wc -l < "$OUT/allowed.txt" | tr -d ' '))"
status=0
if [ -n "$new" ]; then
	echo "skip-census: FAIL — these tests skipped and are not in the reviewed baseline:"
	echo "$new" | sed 's/^/  /'
	echo "  A test that stopped running must be fixed; a new environment gate is reviewed, then added to scripts/skip_census_baseline.txt."
	status=1
fi
if [ "$suite_status" -ne 0 ]; then
	# A failed suite may not have run what the baseline lists (a package that
	# did not build runs nothing), so no "remove it" advice is given.
	echo "skip-census: FAIL — the suite itself failed (go test exit $suite_status):"
	grep -E '"Action":"fail"' "$OUT/suite.json" | grep '"Test":"' |
		sed -E 's/.*"Package":"([^"]*)".*"Test":"([^"]*)".*/  test    \1 \2/' | sort -u | head -40
	grep -E '"Action":"fail"' "$OUT/suite.json" | grep -v '"Test":"' |
		sed -E 's/.*"Package":"([^"]*)".*/  package \1 (build, TestMain or timeout — see below)/' | sort -u
	# Build errors arrive as "build-output" events in the JSON stream (and
	# older toolchains print them on stderr): show both.
	if grep -q '"Action":"build-output"' "$OUT/suite.json"; then
		echo "  --- build output ---"
		grep '"Action":"build-output"' "$OUT/suite.json" |
			sed -E 's/.*"Output":"(.*)"}$/\1/; s/\\n$//; s/\\t/	/g; s/\\"/"/g' | head -40 | sed 's/^/  /'
	fi
	if [ -s "$OUT/stderr.txt" ]; then
		echo "  --- go test stderr ---"
		tail -40 "$OUT/stderr.txt" | sed 's/^/  /'
	fi
	keep_out=1
	echo "  raw output kept in $OUT (suite.json, stderr.txt)"
	status=1
elif [ -n "$gone" ]; then
	echo "skip-census: these reviewed entries ran this time — remove them from the baseline (it only shrinks):"
	echo "$gone" | sed 's/^/  /'
fi
exit $status

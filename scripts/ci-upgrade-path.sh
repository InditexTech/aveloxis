#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
# SPDX-License-Identifier: MIT
#
# ci-upgrade-path.sh — the upgrade a host actually performs, run in CI
# (0.29.107, after the PR #238 loops): the BASE release migrates an empty
# database and populates it, then the HEAD release migrates that database
# the way the deploy ladders do — plain, then --skip-views, then plain
# again — and the stamp, the seeded rows and data-verify must all hold.
# The empty-database golden (TestMigrateStepOrderGolden) cannot see a
# same-release column+index ordering (SR-8), a CONCURRENTLY index over
# existing rows (SR-2) or a once-only step that is not; this can.
#
# Inputs (environment): BASE_BIN and HEAD_BIN (built binaries), CFG (an
# aveloxis.json whose database block names an EMPTY database), PGURL (a
# libpq URL for psql to the same database), GITHUB_TOKEN (the API key is
# one of the seeded rows the upgrade must keep, and the base release's
# add-repo refuses to run without one), SAME_VERSION_OK (set to "true" on
# a push: a head that still carries the base's version has nothing to
# upgrade and answers a notice, not a failure). Exit 1 on any defect.
set -euo pipefail
: "${BASE_BIN:?set BASE_BIN to the base release binary}"
: "${HEAD_BIN:?set HEAD_BIN to the binary under test}"
: "${CFG:?set CFG to the config file naming the scratch database}"
: "${PGURL:?set PGURL to the psql URL of that database}"
: "${GITHUB_TOKEN:?the API key is a seeded row; the token is only handed to add-key, never printed}"
: "${SAME_VERSION_OK:=false}"

q() { psql "$PGURL" -X -q -v ON_ERROR_STOP=1 -At -c "$1"; }
fail() { echo "upgrade-path: FAIL — $*" >&2; exit 1; }
version_of() { "$1" version | sed 's/^aveloxis v//'; }

base_version=$(version_of "$BASE_BIN")
head_version=$(version_of "$HEAD_BIN")
echo "upgrade-path: base $base_version → head $head_version"
if [ "$base_version" = "$head_version" ]; then
	if [ "$SAME_VERSION_OK" = "true" ]; then
		echo "::notice::upgrade-path: base and head are both $head_version; nothing to upgrade on this push"
		exit 0
	fi
	fail "base and head are the same version ($head_version): the version was not bumped"
fi

echo "== base release migrates the empty database"
"$BASE_BIN" migrate -c "$CFG"
base_stamp=$(q "SELECT schema_version FROM aveloxis_ops.schema_meta WHERE id = TRUE")
[ "$base_stamp" = "$base_version" ] || fail "the base migrate stamped '$base_stamp', not $base_version"

echo "== base release populates it (an API key, an administrator, a group, a queued repository)"
"$BASE_BIN" add-key "$GITHUB_TOKEN" --platform github --name ci -c "$CFG" >/dev/null
keys_before=$(q "SELECT count(*) FROM aveloxis_ops.worker_oauth")
[ "$keys_before" -ge 1 ] || fail "add-key on the base release stored no key"
sql="INSERT INTO aveloxis_ops.users (login_name, admin, oauth_provider) VALUES ('ci-upgrade-admin', TRUE, 'github') RETURNING user_id"
uid=$(q "$sql")
sql="INSERT INTO aveloxis_ops.user_groups (user_id, name, status) VALUES ($uid, 'ci upgrade group', 'approved') RETURNING group_id"
gid=$(q "$sql")
"$BASE_BIN" add-repo https://github.com/aveloxis/aveloxis -c "$CFG"
repos_before=$(q "SELECT count(*) FROM aveloxis_data.repos")
[ "$repos_before" -ge 1 ] || fail "add-repo on the base release wrote no repository row"
queue_before=$(q "SELECT count(*) FROM aveloxis_ops.collection_queue")

echo "== head release migrates the populated database: plain, --skip-views, plain (idempotent)"
"$HEAD_BIN" migrate -c "$CFG"
"$HEAD_BIN" migrate --skip-views -c "$CFG"
"$HEAD_BIN" migrate -c "$CFG"
stamp=$(q "SELECT schema_version FROM aveloxis_ops.schema_meta WHERE id = TRUE")
[ "$stamp" = "$head_version" ] || fail "after the upgrade the stamp is '$stamp', not $head_version"

echo "== what the base release wrote is still there"
# count_is WANT SQL: the one number the query answers equals WANT.
count_is() {
	local want=$1 sql=$2 got
	got=$(q "$sql")
	[ "$got" = "$want" ] || fail "$sql = $got; want $want"
}
count_is 1 "SELECT count(*) FROM aveloxis_ops.users WHERE user_id = $uid AND admin"
count_is 1 "SELECT count(*) FROM aveloxis_ops.user_groups WHERE group_id = $gid AND status = 'approved'"
count_is "$repos_before" "SELECT count(*) FROM aveloxis_data.repos"
count_is "$queue_before" "SELECT count(*) FROM aveloxis_ops.collection_queue"
count_is "$keys_before" "SELECT count(*) FROM aveloxis_ops.worker_oauth"
invalid=$(q "SELECT count(*) FROM pg_index WHERE NOT indisvalid")
[ "$invalid" = 0 ] || fail "$invalid invalid index(es) after the upgrade (an interrupted or failed CREATE INDEX CONCURRENTLY)"

echo "== data-verify on the upgraded database"
"$HEAD_BIN" data-verify -c "$CFG"
echo "upgrade-path: OK ($base_version → $head_version)"

#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
# SPDX-License-Identifier: MIT
#
# ci-process-contracts.sh — the three processes a deployment runs (serve,
# api, web) started from one binary against one database, and the
# contracts BETWEEN them exercised over HTTP (0.29.107, after the PR #238
# loops: the sign-out race, the unbounded form parse and the per-request
# privilege read all lived between processes that every test faked):
#
#   - the API refuses an anonymous call (require_auth, no exempt network);
#   - a web session (minted straight into the store — CI has no OAuth
#     app) exchanges for a Bearer token at POST /auth/token, cross-origin
#     refused, GET refused;
#   - the web guard refuses a multipart body (415) and an over-size form
#     (413) before any gate;
#   - privilege is read per request: an administrator demoted in the
#     database is a 403 on the next request;
#   - signing out in the WEB process is a 401 in the API process at once;
#   - serve starts on the migrated database (its own migrate is a no-op:
#     the stamp stays), stays up, and every process stops cleanly on
#     SIGTERM with no panic and no data race in its log.
#
# Inputs (environment): BIN, CFG (database + api + web blocks; see
# integration.yml for the one CI uses), PGURL (psql URL of that database),
# GITHUB_TOKEN (serve refuses to start with no API key; the token is only
# handed to add-key, never printed), WEB_ADDR / API_ADDR (host:port the
# config names), LOGDIR (where the three logs go).
set -euo pipefail
: "${BIN:?}" "${CFG:?}" "${PGURL:?}" "${GITHUB_TOKEN:?serve needs one API key}"
: "${WEB_ADDR:=127.0.0.1:8082}" "${API_ADDR:=127.0.0.1:8383}" "${LOGDIR:=$(mktemp -d)}"
WEB="http://$WEB_ADDR"; API="http://$API_ADDR"
mkdir -p "$LOGDIR"

q() { psql "$PGURL" -X -q -v ON_ERROR_STOP=1 -At -c "$1"; }
fail() { echo "process-contracts: FAIL — $*" >&2; exit 1; }
# expect STATUS curl-args…: the status line of one request, no body.
expect() {
	local want=$1; shift
	local got
	got=$(curl -s -o /dev/null -w '%{http_code}' "$@")
	[ "$got" = "$want" ] || fail "${*: -1} answered $got; want $want" # the arguments carry the cookie and the token: never printed
	echo "  ok $want ${*: -1}"
}
wait_for() {
	local url=$1 i
	for i in $(seq 1 60); do
		if curl -fsS --max-time 5 -o /dev/null "$url"; then return 0; fi
		sleep 1
	done
	fail "$url not ready after 60 s"
}

echo "== migrate, one API key, three processes"
"$BIN" migrate -c "$CFG"
stamp=$(q "SELECT schema_version FROM aveloxis_ops.schema_meta WHERE id = TRUE")
"$BIN" add-key "$GITHUB_TOKEN" --platform github --name ci -c "$CFG" >/dev/null
"$BIN" serve --workers 1 -c "$CFG" >"$LOGDIR/serve.log" 2>&1 & SERVE_PID=$!
"$BIN" api -c "$CFG" >"$LOGDIR/api.log" 2>&1 & API_PID=$!
"$BIN" web -c "$CFG" >"$LOGDIR/web.log" 2>&1 & WEB_PID=$!
cleanup() { kill "$SERVE_PID" "$API_PID" "$WEB_PID" 2>/dev/null || true; }
trap cleanup EXIT
wait_for "$API/api/v1/health"
wait_for "$WEB/login"
# serve's start-up (its migrate — the stamp fast path on a migrated
# database — then the scheduler) has run once its log says so; a fixed
# sleep made the stamp check vacuous on a slow runner (review round 1 of
# 0.29.107).
for i in $(seq 1 60); do
	grep -q "scheduler started" "$LOGDIR/serve.log" && break
	kill -0 "$SERVE_PID" 2>/dev/null || { cat "$LOGDIR/serve.log" >&2; fail "serve exited during start-up"; }
	sleep 1
done
grep -q "scheduler started" "$LOGDIR/serve.log" || { cat "$LOGDIR/serve.log" >&2; fail "serve did not start its scheduler within 60 s"; }
after_serve=$(q "SELECT schema_version FROM aveloxis_ops.schema_meta WHERE id = TRUE")
[ "$after_serve" = "$stamp" ] || fail "serve's own migrate changed the stamp set by migrate ($stamp → $after_serve)"

echo "== the API refuses an anonymous call"
expect 401 "$API/api/v1/groups"

echo "== a web session exchanges for a Bearer token"
raw=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
sql="INSERT INTO aveloxis_ops.users (login_name, admin, oauth_provider) VALUES ('ci-e2e-admin', TRUE, 'github') RETURNING user_id"
uid=$(q "$sql")
q "INSERT INTO aveloxis_ops.user_session_tokens (token, user_id, created_at, expiration, token_hashed, kind)
   VALUES ('sha256\$' || encode(sha256('$raw'::bytea), 'hex'), $uid, extract(epoch from now())::bigint, extract(epoch from now())::bigint + 3600, TRUE, 'web')" >/dev/null
COOKIE="Cookie: aveloxis_session=$raw"
ORIGIN="Origin: $WEB"
expect 405 "$WEB/auth/token"
expect 403 -X POST -H "$COOKIE" -H "Origin: https://evil.example" "$WEB/auth/token"
token=$(curl -fsS -X POST -H "$COOKIE" -H "$ORIGIN" "$WEB/auth/token" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
[ -n "$token" ] || fail "POST /auth/token minted no token"
BEARER="Authorization: Bearer $token"
expect 200 -H "$BEARER" "$API/api/v1/groups"

echo "== the web guard bounds and types form bodies before any gate"
expect 415 -X POST -H "$COOKIE" -H "$ORIGIN" -F "repo_urls=https://github.com/a/b" "$WEB/groups/add-repo"
big=$LOGDIR/big.form
{ printf 'repo_urls='; head -c 5000000 /dev/zero | tr '\0' 'a'; } >"$big"
expect 413 -X POST -H "$COOKIE" -H "$ORIGIN" -H 'Content-Type: application/x-www-form-urlencoded' --data-binary "@$big" "$WEB/groups/add-repo"

echo "== privilege is read per request"
expect 200 -H "$COOKIE" "$WEB/admin/users"
q "UPDATE aveloxis_ops.users SET admin = FALSE WHERE user_id = $uid" >/dev/null
expect 403 -H "$COOKIE" "$WEB/admin/users"

echo "== signing out in web is a 401 in api at once"
expect 302 -X POST -H "$COOKIE" -H "$ORIGIN" "$WEB/logout"
expect 401 -H "$BEARER" "$API/api/v1/groups"
expect 302 -X POST -H "$COOKIE" -H "$ORIGIN" "$WEB/auth/token" # the gate sends the signed-out browser to /login

echo "== every process stops cleanly on SIGTERM (within 60 s)"
kill -TERM "$SERVE_PID" "$API_PID" "$WEB_PID"
for i in $(seq 1 60); do
	if ! kill -0 "$SERVE_PID" 2>/dev/null && ! kill -0 "$API_PID" 2>/dev/null && ! kill -0 "$WEB_PID" 2>/dev/null; then break; fi
	sleep 1
done
for p in "$SERVE_PID" "$API_PID" "$WEB_PID"; do
	if kill -0 "$p" 2>/dev/null; then kill -KILL "$p" 2>/dev/null || true; fail "pid $p was still running 60 s after SIGTERM (killed; see $LOGDIR)"; fi
	if ! wait "$p"; then fail "pid $p exited non-zero on SIGTERM (see $LOGDIR)"; fi
done
trap - EXIT
for log in serve api web; do
	if grep -q -E 'panic:|DATA RACE' "$LOGDIR/$log.log"; then
		cat "$LOGDIR/$log.log" >&2
		fail "$log logged a panic or a data race"
	fi
done
echo "process-contracts: OK (logs in $LOGDIR)"

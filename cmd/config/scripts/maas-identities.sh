#!/usr/bin/env bash
# maas-identities.sh — mint and revoke simulated MaaS tenants for the
# maas-gateway-perf workload.
#
# kube-burner reads this from the embedded FS and runs it as `bash -s - <args>`,
# so positional parameters start at $1 and the exec bit is irrelevant.
#
# Usage:
#   maas-identities.sh mint   <count> <secret> <secret-ns> <metering-ns> [port]
#   maas-identities.sh revoke          <secret> <secret-ns> <metering-ns> [port]
#
# mint   creates <count> users+keys through the metering partner API and stores
#        them in Secret <secret> as keys.txt (user<TAB>key) and users.txt.
# revoke reads users.txt back out of the Secret, deactivates every user, and
#        deletes the Secret.
#
# Users are tagged origin=kube-burner-maas-perf. mint sweeps leftovers carrying
# that tag before minting, so a run aborted by a deadline or Ctrl-C cannot
# accumulate orphans — the teardown hook is best-effort, this sweep is not.
set -euo pipefail

MARKER="kube-burner-maas-perf"
ACTION="${1:-}"

die() { echo "[maas-identities] FATAL: $*" >&2; exit 1; }
info() { echo "[maas-identities] $*"; }

for dep in oc python3; do
  command -v "$dep" >/dev/null 2>&1 || die "$dep is required on the kube-burner host"
done

PF_PID=""
cleanup_pf() {
  if [ -n "$PF_PID" ]; then
    kill "$PF_PID" 2>/dev/null || true
    wait "$PF_PID" 2>/dev/null || true
    PF_PID=""
  fi
}
trap cleanup_pf EXIT INT TERM

start_port_forward() {
  local ns=$1 port=$2
  oc -n "$ns" port-forward svc/metering-service "${port}:8080" >/dev/null 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do
    if python3 -c "
import socket,sys
s=socket.socket(); s.settimeout(1)
sys.exit(0 if s.connect_ex(('127.0.0.1',${port}))==0 else 1)
" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  die "port-forward to metering-service in namespace ${ns} did not become ready"
}

api_token() {
  local ns=$1
  oc -n "$ns" get secret metering-user-management-api -o jsonpath='{.data.token}' 2>/dev/null \
    | python3 -c 'import base64,sys; d=sys.stdin.read().strip(); print(base64.b64decode(d).decode() if d else "")'
}

# Shared python helper. Stdin is taken by the script itself under `bash -s -`,
# so it is written out rather than piped.
write_helper() {
  # BSD mktemp rejects a suffix after XXXXXX, so place the helper in a temp dir.
  HELPER="$WORK/helper.py"
  cat > "$HELPER" <<'PYEOF'
import json, os, sys, threading, urllib.error, urllib.request, uuid

API = os.environ["MAAS_API"]
TOKEN = os.environ["MAAS_TOKEN"]
MARKER = os.environ["MAAS_MARKER"]
HDRS = {"Authorization": f"Bearer {TOKEN}", "Content-Type": "application/json"}


def call(method, path, body=None):
    req = urllib.request.Request(API + path, data=body, headers=HDRS, method=method)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()[:300]
    except Exception as e:  # noqa: BLE001 - surfaced to the caller as a failure
        return None, f"{type(e).__name__}: {e}"


def list_marked():
    users, path = [], "/api/v1/users"
    while path:
        st, body = call("GET", path)
        if st != 200:
            sys.exit(f"listing users failed ({st}): {body}")
        users.extend(body.get("users") or [])
        path = body.get("next_page") or None
    return [u for u in users if (u.get("tags") or {}).get("origin") == MARKER]


def run_pool(items, worker, workers=8):
    idx, lock = [0], threading.Lock()

    def loop():
        while True:
            with lock:
                if idx[0] >= len(items):
                    return
                item = items[idx[0]]
                idx[0] += 1
            worker(item)

    threads = [threading.Thread(target=loop) for _ in range(min(workers, max(1, len(items))))]
    for t in threads:
        t.start()
    for t in threads:
        t.join()


def mint(count, out_dir):
    rows, failures, lock = [], [], threading.Lock()

    def one(_):
        uid = str(uuid.uuid4())
        body = json.dumps({
            "user_id": uid,
            "tags": {"email": f"{uid}@{MARKER}.invalid", "first_name": "Load",
                     "last_name": "Test", "origin": MARKER},
        }).encode()
        st, resp = call("POST", "/api/v1/users", body)
        if st not in (200, 201):
            with lock:
                failures.append(f"create user -> {st}: {resp}")
            return
        st, resp = call("POST", f"/api/v1/users/{uid}/keys", b'{"name":"kube-burner"}')
        if st not in (200, 201) or not isinstance(resp, dict) or not resp.get("key"):
            with lock:
                failures.append(f"mint key -> {st}: {resp}")
            return
        with lock:
            rows.append((uid, resp["key"]))

    run_pool(list(range(count)), one)

    if failures:
        uniq = sorted(set(failures))
        print(f"{len(failures)} of {count} mints failed; distinct errors:", file=sys.stderr)
        for f in uniq[:5]:
            print(f"  {f}", file=sys.stderr)
        # metering wraps the maas-api rejection as "502 MaaS key mint failed";
        # the underlying invalid_subscription only appears in metering's log.
        if any(("invalid_subscription" in f) or ("key mint failed" in f) for f in uniq):
            print(
                "\n  Key creation was rejected by maas-api. The usual cause is that no\n"
                "  MaaSSubscription matches these users, so no key can be minted.\n"
                "  Check:  oc -n <metering-ns> logs deploy/metering-service | grep -i mint\n"
                "  Fix:    create a MaaSModelRef + MaaSSubscription (needs the\n"
                "          LLMInferenceService or ExternalModel CRD installed),\n"
                "          or drop --identities to fall back to the ServiceAccount token.",
                file=sys.stderr)
    if not rows:
        sys.exit("no identities were minted")

    with open(os.path.join(out_dir, "keys.txt"), "w") as f:
        for uid, key in rows:
            f.write(f"{uid}\t{key}\n")
    with open(os.path.join(out_dir, "users.txt"), "w") as f:
        for uid, _ in rows:
            f.write(f"{uid}\n")
    print(f"minted {len(rows)} of {count} identities")


def revoke(user_ids):
    ok, lock = [0], threading.Lock()

    def one(uid):
        st, _ = call("DELETE", f"/api/v1/users/{uid}")
        if st in (200, 204):
            with lock:
                ok[0] += 1

    run_pool(user_ids, one)
    print(f"revoked {ok[0]} of {len(user_ids)} identities")


if __name__ == "__main__":
    cmd = sys.argv[1]
    if cmd == "mint":
        mint(int(sys.argv[2]), sys.argv[3])
    elif cmd == "sweep":
        stale = [u["user_id"] for u in list_marked()]
        if stale:
            print(f"sweeping {len(stale)} leftover {MARKER} identities")
            revoke(stale)
        else:
            print("no leftover identities to sweep")
    elif cmd == "revoke":
        with open(sys.argv[2]) as fh:
            revoke([line.strip() for line in fh if line.strip()])
PYEOF
}

case "$ACTION" in
  mint)
    COUNT="${2:?count required}"
    SECRET="${3:?secret name required}"
    SECRET_NS="${4:?secret namespace required}"
    METERING_NS="${5:?metering namespace required}"
    PORT="${6:-18099}"

    WORK=$(mktemp -d "${TMPDIR:-/tmp}/maas-identities-XXXXXX")
    chmod 700 "$WORK"
    start_port_forward "$METERING_NS" "$PORT"
    TOKEN=$(api_token "$METERING_NS")
    [ -n "$TOKEN" ] || die "could not read secret metering-user-management-api in ${METERING_NS}"

    export MAAS_API="http://127.0.0.1:${PORT}" MAAS_TOKEN="$TOKEN" MAAS_MARKER="$MARKER"
    write_helper

    python3 "$HELPER" sweep
    info "minting ${COUNT} identities"
    python3 "$HELPER" mint "$COUNT" "$WORK"

    # beforeAllJobs runs before kube-burner creates any namespace.
    oc create namespace "$SECRET_NS" --dry-run=client -o yaml | oc apply -f - >/dev/null
    oc -n "$SECRET_NS" delete secret "$SECRET" --ignore-not-found >/dev/null 2>&1 || true
    oc -n "$SECRET_NS" create secret generic "$SECRET" \
      --from-file=keys.txt="$WORK/keys.txt" \
      --from-file=users.txt="$WORK/users.txt" >/dev/null
    oc -n "$SECRET_NS" label secret "$SECRET" "origin=$MARKER" --overwrite >/dev/null 2>&1 || true
    info "stored $(wc -l < "$WORK/keys.txt" | tr -d ' ') identities in secret ${SECRET_NS}/${SECRET}"

    rm -rf "$WORK"
    ;;

  revoke)
    SECRET="${2:?secret name required}"
    SECRET_NS="${3:?secret namespace required}"
    METERING_NS="${4:?metering namespace required}"
    PORT="${5:-18099}"

    if ! oc -n "$SECRET_NS" get secret "$SECRET" >/dev/null 2>&1; then
      info "secret ${SECRET_NS}/${SECRET} not found; nothing to revoke"
      exit 0
    fi

    WORK=$(mktemp -d "${TMPDIR:-/tmp}/maas-identities-XXXXXX")
    chmod 700 "$WORK"
    oc -n "$SECRET_NS" get secret "$SECRET" -o jsonpath='{.data.users\.txt}' \
      | python3 -c 'import base64,sys; sys.stdout.write(base64.b64decode(sys.stdin.read().strip()).decode())' \
      > "$WORK/users.txt"

    start_port_forward "$METERING_NS" "$PORT"
    TOKEN=$(api_token "$METERING_NS")
    [ -n "$TOKEN" ] || die "could not read secret metering-user-management-api in ${METERING_NS}"

    export MAAS_API="http://127.0.0.1:${PORT}" MAAS_TOKEN="$TOKEN" MAAS_MARKER="$MARKER"
    write_helper
    python3 "$HELPER" revoke "$WORK/users.txt"

    oc -n "$SECRET_NS" delete secret "$SECRET" --ignore-not-found >/dev/null
    info "deleted secret ${SECRET_NS}/${SECRET}"
    rm -rf "$WORK"
    ;;

  sweep)
    # Manual recovery: revoke every identity carrying the marker tag, even when
    # the Secret holding users.txt is gone.
    METERING_NS="${2:?metering namespace required}"
    PORT="${3:-18099}"

    WORK=$(mktemp -d "${TMPDIR:-/tmp}/maas-identities-XXXXXX")
    chmod 700 "$WORK"
    start_port_forward "$METERING_NS" "$PORT"
    TOKEN=$(api_token "$METERING_NS")
    [ -n "$TOKEN" ] || die "could not read secret metering-user-management-api in ${METERING_NS}"

    export MAAS_API="http://127.0.0.1:${PORT}" MAAS_TOKEN="$TOKEN" MAAS_MARKER="$MARKER"
    write_helper
    python3 "$HELPER" sweep
    rm -rf "$WORK"
    ;;

  *)
    die "usage: maas-identities.sh <mint|revoke|sweep> ..."
    ;;
esac

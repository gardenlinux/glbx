#!/usr/bin/env bash
set -euo pipefail

# glbx end-to-end acceptance test: drive the whole pipeline from an empty tree
# to a built image, then smoke-test it inside the sandbox.
#
# Usage:
#   ./full_build_test.sh            # all phases
#   ./full_build_test.sh import     # prepare staging only (import + lockfiles)
#   ./full_build_test.sh graph      # prepare + render graph
#   ./full_build_test.sh build      # prepare + graph + build
#   ./full_build_test.sh verify     # all phases incl. exec-chroot smoke test
#
# Required environment (provided by `make e2e`):
#   GLBX_BIN             — path to the glbx binary
#   GLBX_EXEC_ENV_STUB   — path to exec_env_stub
# Optional:
#   GLBX_WORK_DIR    — work directory (default: a fresh mktemp -d, removed on exit)
#   GLBX_KEEP_WORK   — set to 1 to keep the work dir on exit

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

GLBX="${GLBX_BIN:-$PROJECT_DIR/bin/glbx}"
STUB="${GLBX_EXEC_ENV_STUB:-$PROJECT_DIR/bin/exec_env_stub}"

if [ ! -x "$GLBX" ] || [ ! -x "$STUB" ]; then
	echo "ERROR: glbx or stub not built; run \`make build\` (or \`make e2e\`)." >&2
	exit 1
fi

PHASE="${1:-all}"

if [ -n "${GLBX_WORK_DIR:-}" ]; then
	WORK_DIR="$GLBX_WORK_DIR"
	mkdir -p "$WORK_DIR"
else
	WORK_DIR="$(mktemp -d /tmp/glbx-e2e-XXXXXX)"
fi
CACHE_DIR="$WORK_DIR/cache"
STAGING_DIR="$WORK_DIR/staging"
mkdir -p "$CACHE_DIR" "$STAGING_DIR"

cleanup() {
	if [ "${GLBX_KEEP_WORK:-}" = "1" ]; then
		echo "Keeping work dir: $WORK_DIR"
	else
		rm -rf "$WORK_DIR"
	fi
}
trap cleanup EXIT

echo "=== glbx acceptance test ==="
echo "Phase:   $PHASE"
echo "Work:    $WORK_DIR"
echo "glbx:    $GLBX"
echo ""

phase_prepare() {
	GLBX_CACHE="$CACHE_DIR" GLBX_BIN="$GLBX" GLBX_EXEC_ENV_STUB="$STUB" \
		"$SCRIPT_DIR/prepare_staging.sh" "$STAGING_DIR"
}

phase_graph() {
	echo "=== PHASE: dependency graph ==="
	GRAPH_OUTPUT="$WORK_DIR/graph.md"
	"$GLBX" graph --cache "$CACHE_DIR" --conf-dir "$STAGING_DIR" --stub "$STUB" --output "$GRAPH_OUTPUT" \
		|| { echo "FAIL: graph"; exit 1; }
	echo "Written to: $GRAPH_OUTPUT"
}

phase_build() {
	echo "=== PHASE: build artifact graph ==="
	"$GLBX" build --cache "$CACHE_DIR" --conf-dir "$STAGING_DIR" --stub "$STUB" \
		|| { echo "FAIL: build"; exit 1; }

	BUILD_SUMMARY=$("$GLBX" build --cache "$CACHE_DIR" --conf-dir "$STAGING_DIR" --stub "$STUB" 2>/dev/null)
	ROOTFS_ID=$(echo "$BUILD_SUMMARY" | grep "rootfs identity:" | awk '{print $NF}')
	if [ -z "$ROOTFS_ID" ]; then
		echo "FAIL: could not determine rootfs identity"
		exit 1
	fi
	echo "Rootfs identity: $ROOTFS_ID"
}

phase_verify() {
	[ -z "${ROOTFS_ID:-}" ] && phase_build
	echo "=== PHASE: exec-chroot smoke test ==="
	EXEC_OUTPUT=$("$GLBX" exec-chroot --cache "$CACHE_DIR" "$ROOTFS_ID" bash -c 'echo ACCEPTANCE_OK && ls /usr/bin/cat && id' 2>&1) || {
		echo "FAIL: exec-chroot"; echo "$EXEC_OUTPUT"; exit 1;
	}
	echo "$EXEC_OUTPUT" | grep -q "ACCEPTANCE_OK" || { echo "FAIL: unexpected exec-chroot output"; echo "$EXEC_OUTPUT"; exit 1; }
	echo "$EXEC_OUTPUT"
}

case "$PHASE" in
	import|lockfile) phase_prepare ;;
	graph)           phase_prepare; phase_graph ;;
	build)           phase_prepare; phase_graph; phase_build ;;
	verify|all)      phase_prepare; phase_graph; phase_build; phase_verify
	                 echo "=== ACCEPTANCE COMPLETE: image built entirely from source ===" ;;
	*) echo "Unknown phase: $PHASE"; echo "Usage: $0 [import|graph|build|verify|all]"; exit 1 ;;
esac

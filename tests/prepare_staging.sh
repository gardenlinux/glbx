#!/usr/bin/env bash
set -euo pipefail

# Prepare a working-tree directory for glbx builds: import each source package,
# apply any template patches, place its build.yml, and generate the per-package
# and image-configuration locks. After it completes, the directory is ready for
# `glbx build`.
#
# When the directory is a git repository, the integration artifacts this script
# produces on top of the pristine imports — applied patches, per-package build
# config and locks, and the rootfs configuration — are each committed, so a
# prepared tree leaves nothing uncommitted. (glbx import records the pristine
# imports itself; the lockfile commands never touch git.) Outside a git
# repository these commit steps are skipped.
#
# Usage:
#   ./prepare_staging.sh <conf-dir>   # prepare a specific directory
#   ./prepare_staging.sh              # defaults to ../staging
#
# Required environment:
#   GLBX_BIN             — path to the glbx binary (built by `make build`)
#   GLBX_EXEC_ENV_STUB   — path to the exec_env_stub binary (built by `make build`)
# Optional:
#   GLBX_CACHE           — object-store cache (default: the glbx default)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATES_DIR="$SCRIPT_DIR/templates"

CONF_DIR="${1:-$(cd "$PROJECT_DIR/.." && pwd)/staging}"
mkdir -p "$CONF_DIR"
CONF_DIR="$(cd "$CONF_DIR" && pwd)"
PKGS_DIR="$CONF_DIR/pkgs"
mkdir -p "$PKGS_DIR"

# Whether the staging directory is a git repository; when it is, integration
# artifacts are committed as they are produced.
IS_GIT=0
if git -C "$CONF_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	IS_GIT=1
fi

# git_commit <message> <pathspec...> stages the given paths and commits them,
# but only in a git repository and only when they produced a change.
git_commit() {
	[ "$IS_GIT" = 1 ] || return 0
	local msg="$1"; shift
	git -C "$CONF_DIR" add -- "$@"
	git -C "$CONF_DIR" diff --cached --quiet && return 0
	git -C "$CONF_DIR" commit -q -m "$msg"
}

GLBX="${GLBX_BIN:-$PROJECT_DIR/bin/glbx}"
STUB="${GLBX_EXEC_ENV_STUB:-$PROJECT_DIR/bin/exec_env_stub}"

if [ ! -x "$GLBX" ] || [ ! -x "$STUB" ]; then
	echo "ERROR: glbx or stub not built; run \`make build\`." >&2
	echo "  GLBX=$GLBX" >&2
	echo "  STUB=$STUB" >&2
	exit 1
fi

echo "=== glbx prepare staging ==="
echo "Conf dir: $CONF_DIR"
echo "glbx:     $GLBX"
echo ""

CACHE_ARGS=()
if [ -n "${GLBX_CACHE:-}" ]; then
	CACHE_ARGS=(--cache "$GLBX_CACHE")
fi

# One cookie so every invocation in this run shares a single InRelease fetch.
COOKIE="$(uuidgen)"

PACKAGES=()
for dir in "$TEMPLATES_DIR"/*/; do
	[ -f "$dir/build.yml" ] || continue
	PACKAGES+=("$(basename "$dir")")
done
echo "=== Source packages (${#PACKAGES[@]}): ${PACKAGES[*]} ==="

echo "=== Import sources ==="
for pkg in "${PACKAGES[@]}"; do
	if [ -d "$PKGS_DIR/$pkg/src" ]; then
		echo "$pkg: already imported"
		continue
	fi
	echo "Importing $pkg..."
	"$GLBX" import "${CACHE_ARGS[@]}" --cookie "$COOKIE" --output "$CONF_DIR" --no-verify "$pkg" \
		|| { echo "FAIL: import $pkg"; exit 1; }
done

echo "=== Apply template patches ==="
for pkg in "${PACKAGES[@]}"; do
	patches_dir="$TEMPLATES_DIR/$pkg/patches"
	[ -d "$patches_dir" ] || continue
	src_dir="$PKGS_DIR/$pkg/src"
	stamp="$PKGS_DIR/$pkg/.patches-applied"
	# In a git repo the "patched <pkg>" commit is the record of applied patches;
	# outside git a stamp file guards against re-applying on a re-run.
	if [ "$IS_GIT" = 1 ]; then
		if git -C "$CONF_DIR" log --format=%s -- "pkgs/$pkg" 2>/dev/null | grep -qxF "patched $pkg"; then
			echo "$pkg: patches already applied"; continue
		fi
	elif [ -f "$stamp" ]; then
		echo "$pkg: patches already applied"; continue
	fi
	shopt -s nullglob
	patches=("$patches_dir"/*.patch)
	shopt -u nullglob
	[ ${#patches[@]} -gt 0 ] || continue
	IFS=$'\n' patches=($(printf '%s\n' "${patches[@]}" | sort)); unset IFS
	for p in "${patches[@]}"; do
		echo "$pkg: applying $(basename "$p")"
		patch -p1 -d "$src_dir" --no-backup-if-mismatch < "$p" || { echo "FAIL: patch $p"; exit 1; }
	done
	if [ "$IS_GIT" = 1 ]; then
		git_commit "patched $pkg" "pkgs/$pkg"
	else
		touch "$stamp"
	fi
done

echo "=== Build config and lockfiles ==="
for pkg in "${PACKAGES[@]}"; do
	if [ -f "$PKGS_DIR/$pkg/build-deps.yml" ]; then
		echo "$pkg: lockfile exists"
		continue
	fi
	[ -f "$TEMPLATES_DIR/$pkg/build.yml" ] && cp "$TEMPLATES_DIR/$pkg/build.yml" "$PKGS_DIR/$pkg/build.yml"
	echo "Generating lockfile for $pkg..."
	"$GLBX" lockfile "${CACHE_ARGS[@]}" --cookie "$COOKIE" --output "$CONF_DIR" "$pkg" \
		|| { echo "FAIL: lockfile $pkg"; exit 1; }
	# build.yml and its resolved lock are one logical change — one commit.
	git_commit "configure $pkg build" "pkgs/$pkg"
done

if [ -f "$CONF_DIR/rootfs-deps.yml" ]; then
	echo "rootfs lockfile exists"
else
	[ -f "$TEMPLATES_DIR/rootfs.yml" ] && cp "$TEMPLATES_DIR/rootfs.yml" "$CONF_DIR/rootfs.yml"
	echo "Generating rootfs lockfile..."
	"$GLBX" lockfile-rootfs "${CACHE_ARGS[@]}" --cookie "$COOKIE" --output "$CONF_DIR" \
		|| { echo "FAIL: lockfile-rootfs"; exit 1; }
	git_commit "configure rootfs image" rootfs.yml rootfs-deps.yml
fi

echo "=== Staging prepared: $CONF_DIR ==="
echo "Build with: $GLBX build --conf-dir $CONF_DIR --stub $STUB"

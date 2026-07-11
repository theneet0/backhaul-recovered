#!/usr/bin/env bash
set -Eeuo pipefail

REPOSITORY="${1:-theneet0/backhaul-recovered}"
RELEASE_TAG="${BACKHAUL_RELEASE_TAG:-v2.0.0-hotfix8-recovered.3}"

command -v gh >/dev/null 2>&1 || {
  echo "GitHub CLI (gh) is required." >&2
  exit 1
}
gh auth status >/dev/null

case "$REPOSITORY" in
  */*) ;;
  *) echo "Repository must use OWNER/NAME format." >&2; exit 2 ;;
esac

if ! gh repo view "$REPOSITORY" >/dev/null 2>&1; then
  gh repo create "$REPOSITORY" \
    --public \
    --description "Privacy-clean Backhaul behavioral reconstruction with verified quick install" \
    --disable-wiki
fi

REMOTE_URL="https://github.com/${REPOSITORY}.git"
if git remote get-url origin >/dev/null 2>&1; then
  git remote set-url origin "$REMOTE_URL"
else
  git remote add origin "$REMOTE_URL"
fi

git push -u origin main
if git rev-parse "$RELEASE_TAG" >/dev/null 2>&1; then
  git push origin "$RELEASE_TAG"
else
  git tag -a "$RELEASE_TAG" -m "$RELEASE_TAG"
  git push origin "$RELEASE_TAG"
fi

echo "Published ${REPOSITORY}; GitHub Actions will build and publish ${RELEASE_TAG}."

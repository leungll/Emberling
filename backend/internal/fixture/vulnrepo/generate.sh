#!/bin/sh
# Creates the local git repository the vulnerability-fix demo binds an Agent workspace to,
# from the source tree in ./repo, and tags its single commit "baseline".
#
#   sh backend/internal/fixture/vulnrepo/generate.sh <target-dir>
#
# The commit is reproducible: author, committer, dates and message are fixed, so the same
# tree always yields the same commit id, which a Definition names as the workspace's
# baseCommit. Running the script again against an existing target is safe: a target that
# already holds the same baseline commit is left untouched; one that holds anything else is
# replaced, because the target is a generated artifact, never a place where work is kept.
# The repository is read by the workspace Tools and the sandbox runner through `git archive`
# only; nothing ever commits to it after generation.
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <target-dir>" >&2
  exit 2
fi
target=$1
here=$(cd "$(dirname "$0")" && pwd)
source="$here/repo"

if ! command -v git >/dev/null 2>&1; then
  echo "generate.sh: git is required" >&2
  exit 1
fi

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0
export GIT_AUTHOR_NAME="Token Service Maintainers" GIT_AUTHOR_EMAIL="maintainers@example.com"
export GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME" GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"
export GIT_AUTHOR_DATE="2026-10-01T09:00:00Z" GIT_COMMITTER_DATE="2026-10-01T09:00:00Z"

staging=$(mktemp -d "${TMPDIR:-/tmp}/vulnrepo.XXXXXX")
trap 'rm -rf "$staging"' EXIT

# cp -R of a directory with a trailing /. copies its contents, not the directory itself.
cp -R "$source/." "$staging/"
git -C "$staging" -c init.defaultBranch=main init -q
git -C "$staging" -c core.autocrlf=false -c core.fileMode=false add -A
git -C "$staging" -c commit.gpgsign=false -c core.fileMode=false commit -q -m "Token service baseline"
git -C "$staging" tag baseline
commit=$(git -C "$staging" rev-parse HEAD)

if [ -e "$target" ]; then
  existing=$(git -C "$target" rev-parse --verify -q baseline 2>/dev/null || true)
  if [ "$existing" = "$commit" ]; then
    echo "$target already holds baseline $commit"
    exit 0
  fi
  rm -rf "$target"
fi
mkdir -p "$(dirname "$target")"
mv "$staging" "$target"
trap - EXIT
echo "$target holds baseline $commit"

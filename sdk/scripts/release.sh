#!/bin/sh
# Releases every SDK at one version by pushing its eight tags (sdk/<lang>/v<version>); release-sdk.yml publishes each.
#
#   sdk/scripts/release.sh 0.2.0
#
# The version must already be on main: set info.version in sdk/spec/openapi.json, run gen (it writes the version into
# every SDK and manifest) and merge that. This script only checks and tags.
set -eu

v=${1:?usage: release.sh <version>}
case $v in
[0-9]*.[0-9]*.[0-9]*) ;;
*) echo "release.sh: version must look like 1.2.3" >&2; exit 1 ;;
esac
cd "$(dirname "$0")/.."

test -z "$(git status --porcelain)" || { echo "release.sh: the working tree is not clean" >&2; exit 1; }
git fetch -q origin main
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" || { echo "release.sh: check out origin/main first" >&2; exit 1; }
grep -q "\"version\": \"$v\"" spec/openapi.json || { echo "release.sh: spec/openapi.json is not at $v" >&2; exit 1; }
(cd scripts/gen && go run .)
test -z "$(git status --porcelain)" || { echo "release.sh: generated code is not at $v; run gen and merge first" >&2; git checkout -q -- .; exit 1; }

tags=""
for lang in go node python ruby php java dotnet rust; do
	tag="sdk/$lang/v$v"
	git rev-parse -q --verify "refs/tags/$tag" >/dev/null && { echo "release.sh: $tag exists" >&2; exit 1; }
	tags="$tags $tag"
done
for tag in $tags; do git tag "$tag"; done
# shellcheck disable=SC2086 # one argument per tag
git push origin $tags
echo "pushed:$tags"

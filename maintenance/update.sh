#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
test "$(git branch --show-current)" = codex/quotio-maintained
test -z "$(git status --porcelain)" || { echo 'Refusing to modify a dirty checkout'; exit 1; }
lock_dir="$(git rev-parse --git-dir)/quotio-maintenance.lock"
mkdir "$lock_dir" || { echo 'Another maintenance run is active'; exit 1; }
build_dir=$(mktemp -d /tmp/quotio-build.XXXXXX)
trap 'rm -r "$build_dir"; rmdir "$lock_dir"' EXIT
git fetch origin codex/quotio-maintained
git merge --ff-only origin/codex/quotio-maintained
release=$(gh api repos/router-for-me/CLIProxyAPI/releases/latest --jq .tag_name)
[[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Unexpected upstream release name'; exit 1; }
git fetch upstream "refs/tags/$release:refs/tags/$release"
git merge --no-edit "$release"
revision=$(git rev-parse HEAD)
version="${release}-quotiofix.${revision:0:12}"
engine_dir="$HOME/Library/Application Support/Quotio/proxy/upstream"
if [[ -f "$engine_dir/$version/BUILD.json" && "$(readlink "$engine_dir/current")" = "$engine_dir/$version" ]]; then
  go run ./cmd/quotio-maintain -check -version "$version" -commit "$revision"
  git push origin HEAD:codex/quotio-maintained
  echo "UP_TO_DATE $version"
  exit 0
fi
for regression in TestRepairResponsesWebsocketToolCallsPreservesNamedUnsolicitedOutputs TestResponsesWebsocketPrewarmPreservesCompactedFollowup; do
  test "$(go test ./sdk/api/handlers/openai -list "^${regression}$" | head -1)" = "$regression"
done
go test ./sdk/api/handlers/openai ./cmd/quotio-maintain -count=1
go build -ldflags "-X main.Version=$version -X main.Commit=$revision" -o "$build_dir/CLIProxyAPI" ./cmd/server
go run ./cmd/quotio-maintain -binary "$build_dir/CLIProxyAPI" -version "$version" -commit "$revision"
git push origin HEAD:codex/quotio-maintained
echo "UPDATED $version"

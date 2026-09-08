# Maintained Quotio engine

This branch tracks stable router-for-me/CLIProxyAPI releases and preserves named standalone Codex inputs and acknowledged synthetic WebSocket warm-up context. Quotio remains the stock macOS app and manages the engine process. Both permanent regressions are mandatory before an upstream build is promoted:

- `TestRepairResponsesWebsocketToolCallsPreservesNamedUnsolicitedOutputs`: keeps named inputs without a paired `call_id`.
- `TestResponsesWebsocketPrewarmPreservesCompactedFollowup`: keeps tools/base instructions when a compacted-history delta refers to a locally acknowledged warm-up. It also covers replacement tools, unrelated parent IDs, invalid-input retry, upstream-failure reconnect and subsequent real-response continuation.

The warm-up repair materializes the exact acknowledged context before compaction-replacement detection. It is scoped to the pending response ID on that connection; genuine replacement transcripts do not inherit stale tools. No task-history changes or report relay are required.

## Update and install

Run `maintenance/update.sh` from the `codex/quotio-maintained` branch in a clean checkout with remotes `origin` (this fork) and `upstream` (router-for-me/CLIProxyAPI). Go 1.26+, Git, GitHub CLI authentication and a running Quotio local proxy are required. The script reads the latest stable upstream release, fetches/merges it while preserving our commits, checks the regression still exists, runs the complete OpenAI handler suite and installer checks, and builds the engine.

The macOS installer starts that candidate with a private temporary configuration on a spare loopback port. It sends a synthetic named input with a Session_id header and requires the exact diagnostic function call. It never sends actual conversation content. On success it installs a separate `vX.Y.Z-quotiofix.COMMIT` directory under Quotio's engine directory, records the source commit and binary digest, switches `current` atomically and asks Quotio to restart its engine by terminating only its verified listener process. Quotio's normal crash-recovery code owns the restart. It then verifies the selected executable and repeats the live named-input test. A failed promotion restores the previous selection and attempts/verifies its restart; any failure is surfaced rather than reported as installed.

The existing application config, local API keys, accounts and Codex task histories are not changed. Candidate configuration and logs are private temporary files deleted on exit. The previous engine is retained. Routine unchanged checks validate the installed identity, binary digest and owning process without making an inference request.

After a successful install, the script pushes the maintained branch. Merge conflicts, tests, build failures, live-probe failures, unexpected port owners or dirty checkouts stop the update. The scheduled maintainer must report failures and preserve the working engine. No force-push, destructive reset, automatic dropping of the regression or assumption that a release fixes the bug is permitted. If upstream fixes the same behavior, remove only the now-redundant runtime change while retaining the regression.

## Local scheduling

A native Codex heartbeat runs this script daily on the user's Mac; GitHub Actions do not install local engines. The recurring prompt is limited to this fork, its tests, and the Quotio engine installation. The app and Mac must be available for local scheduled work. A new upstream release is only adopted after the gates pass; there is no untested auto-upgrade path in this workflow.

Using Quotio's stock engine upgrade action can select an unpatched upstream binary. The next maintained update detects a different `current` selection and reinstalls the tested maintained build. Quotio app updates remain separate and are not disabled.

## Rollback

Select the previously working version in Quotio's engine-version control. Version directories contain `BUILD.json` for maintained releases. Keep the active and previous engine; do not delete them during an update. The initial stock v7.2.136 engine is restored to its original directory when the first maintained release is installed.

Issue: https://github.com/nguyenphutrong/quotio/issues/528

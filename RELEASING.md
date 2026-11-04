# Releasing

`mygo.json` sets no `macos.signingIdentity` and no `macos.notarize`, so `go tool mygo build` signs the app ad hoc: it runs on the Mac that built it, and Gatekeeper blocks it elsewhere (right-click → 打开, or `xattr -dr com.apple.quarantine`). To ship outside this Mac, buy an Apple Developer ID, set `macos.signingIdentity` and `macos.notarize` again, and add the `MACOS_CERTIFICATE`, `MACOS_CERTIFICATE_PASSWORD`, `APPLE_ID`, `APPLE_TEAM_ID` and `APPLE_APP_PASSWORD` secrets the workflow imports.

Updates are signed with the key of `mygo keygen`, `keys/mygo-update.key` in the repository: `updates.privateKey` points at it, so `-upload` needs no secret, and `MYGO_UPDATER_PRIVATE_KEY` in CI takes its place when set. The key is checked against `updates.publicKey`, so replacing one means replacing both. Installed apps accept only updates signed with this key: lose it and you have to ship a new key with a new release people install by hand.

1. Commit pending work, bring main up to date with origin, and run `go test ./internal/...`. Others push to main too, so a release cut from a stale main leaves out their work:

   ```bash
   git pull --rebase origin main
   ```

2. Bump `version` in `mygo.json`, and add a `## 0.1.8` section at the top of `CHANGELOG.md` saying, in Chinese, what changed for people using the app since the last release (`git log v0.1.7..HEAD`, after the pull). Commit both in a commit titled just the version, e.g. `0.1.8`. MyGo's updater shows that section in the app's update window, and the release page takes it as its notes; `mygo build` fails without one.
3. Push main first, and tag only once that push has gone through. Pushing both at once still pushes the tag when main is rejected, and the tag starts a release build of a commit that isn't on main:

   ```bash
   git push origin main
   git tag v0.1.8
   git push origin v0.1.8
   ```

   If main is rejected, pull again, add what came in to the changelog, amend the version commit, test, and push main again before tagging. If a tag went up on the wrong commit anyway, cancel its run, then delete the tag and the draft it made before tagging again:

   ```bash
   git push origin :refs/tags/v0.1.8
   gh release delete v0.1.8 --yes
   ```

4. `.github/workflows/release.yml` builds macOS, Windows and Linux and uploads them to a draft release. Wait for every job to pass:

   ```bash
   gh run watch "$(gh run list --workflow release.yml -L1 --json databaseId -q '.[0].databaseId')" --exit-status
   ```

   If a job fails, don't publish; fix it first.

5. Publish the draft and mark it latest:

   ```bash
   gh release edit v0.1.8 --draft=false --latest
   ```

6. Check the update feed shows the new version:

   ```bash
   curl -sL https://github.com/yxxbc/haruhikage-app/releases/latest/download/update-darwin-arm64.json
   ```

## The speech recognition libraries

Podcast captions run sherpa-onnx, whose libraries the app downloads with the model from the `sherpa-onnx-<version>` release, not from the app's own. They only change with sherpa-onnx's version, in `scripts/sherpa`. Then pack them again, signing the macOS ones with the app's Developer ID (the app's hardened runtime loads no others), commit the `internal/speech/natives.go` it writes, and upload them as a prerelease, which never becomes the latest that updates look for:

```bash
go run ./scripts/sherpa -sign "Developer ID Application: … (TEAMID)"
gh release create sherpa-onnx-2.0.1 --prerelease --latest=false build/sherpa/*
```

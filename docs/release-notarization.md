# macOS notarization runbook

How to make the darwin release binaries pass Gatekeeper, so users who download
them (curl/`install-client.sh`/brew) don't hit *"dejima cannot be opened because
the developer cannot be verified."* You hold an Apple Developer ID already — this
is mostly supplying the cert + a few secrets.

The release pipeline is built to make this a drop-in: `make release-binaries`
already codesigns the darwin binaries when `CODESIGN_IDENTITY` is set (no-op
otherwise). What's left is (a) run that step on a **macOS** runner, (b) feed it
the cert, and (c) add a notarization step.

> **Stapling caveat (read first):** you can staple a notarization ticket to a
> `.app`/`.pkg`/`.dmg`, but **not to a bare Mach-O CLI binary**. For tarball-
> distributed CLIs, you codesign + notarize the binaries (registering them with
> Apple by hash); Gatekeeper then verifies *online* at first run. That's the
> normal trade-off for a CLI. The quarantine-strip in `install-client.sh` stays
> as an offline fallback. If you ever want offline/stapled installs, ship a
> signed `.pkg` instead.

---

## 1. Apple-side setup (one-time)

1. **Developer ID Application certificate.** Apple Developer → Certificates → +
   → *Developer ID Application*. Create, download, double-click to import into
   your login Keychain. Note the identity string:
   ```
   security find-identity -p codesigning -v
   #  → "Developer ID Application: Your Name (TEAMID)"
   ```
2. **Export it as a .p12.** Keychain Access → right-click the cert → Export →
   `.p12`, set an export password. Then base64 it for CI:
   ```
   base64 -i DeveloperID.p12 | pbcopy   # paste into the MACOS_CERTIFICATE_P12 secret
   ```
3. **App Store Connect API key** (for `notarytool`; cleaner than Apple-ID +
   app-specific-password). App Store Connect → Users and Access → Integrations →
   Keys → generate a key with the *Developer* role. Download the `.p8` **once**;
   note the **Key ID** and **Issuer ID**.
   ```
   base64 -i AuthKey_XXXX.p8 | pbcopy    # → APPLE_API_KEY_P8 secret
   ```

## 2. GitHub Actions secrets to add

| Secret | Value |
| --- | --- |
| `MACOS_CERTIFICATE_P12` | base64 of the `.p12` |
| `MACOS_CERTIFICATE_PASSWORD` | the `.p12` export password |
| `MACOS_SIGN_IDENTITY` | `Developer ID Application: Your Name (TEAMID)` |
| `APPLE_API_KEY_ID` | App Store Connect key ID |
| `APPLE_API_ISSUER_ID` | App Store Connect issuer ID |
| `APPLE_API_KEY_P8` | base64 of the `.p8` |

## 3. Workflow changes (drop-in)

In `.github/workflows/release.yml`, switch the runner to macOS (its Go can still
cross-build the linux/windows targets) and add cert import + notarization around
the existing build step:

```yaml
jobs:
  release:
    runs-on: macos-latest          # was ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: actions/setup-go@v5
        with: { go-version: stable }

      - name: Import Developer ID certificate
        env:
          P12: ${{ secrets.MACOS_CERTIFICATE_P12 }}
          P12_PW: ${{ secrets.MACOS_CERTIFICATE_PASSWORD }}
        run: |
          KEYCHAIN="$RUNNER_TEMP/signing.keychain-db"
          security create-keychain -p actions "$KEYCHAIN"
          security set-keychain-settings -lut 21600 "$KEYCHAIN"
          security unlock-keychain -p actions "$KEYCHAIN"
          echo "$P12" | base64 --decode > "$RUNNER_TEMP/cert.p12"
          security import "$RUNNER_TEMP/cert.p12" -k "$KEYCHAIN" -P "$P12_PW" -T /usr/bin/codesign
          security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k actions "$KEYCHAIN"
          security list-keychains -d user -s "$KEYCHAIN" $(security list-keychains -d user | tr -d '"')

      - name: Sanity build + tests
        run: { go build ./...; go test ./...; }

      # CODESIGN_IDENTITY makes `release-binaries` codesign the darwin binaries.
      - name: Build + sign release archives
        run: make release-binaries VERSION=${GITHUB_REF_NAME} CODESIGN_IDENTITY="${{ secrets.MACOS_SIGN_IDENTITY }}"

      - name: Notarize darwin archives
        env:
          KEY_P8: ${{ secrets.APPLE_API_KEY_P8 }}
          KEY_ID: ${{ secrets.APPLE_API_KEY_ID }}
          ISSUER: ${{ secrets.APPLE_API_ISSUER_ID }}
        run: |
          echo "$KEY_P8" | base64 --decode > "$RUNNER_TEMP/key.p8"
          for tgz in dist/dejima_*_darwin_*.tar.gz; do
            work="$RUNNER_TEMP/notarize"; rm -rf "$work"; mkdir -p "$work"
            tar -xzf "$tgz" -C "$work"
            # notarytool wants a zip/pkg/dmg; submit the signed binaries.
            ( cd "$work" && ditto -c -k --keepParent . payload.zip )
            xcrun notarytool submit "$work/payload.zip" \
              --key "$RUNNER_TEMP/key.p8" --key-id "$KEY_ID" --issuer "$ISSUER" --wait
          done

      - name: Publish GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          files: |
            dist/dejima_*.tar.gz
            dist/dejima_*.zip
            dist/SHA256SUMS
          generate_release_notes: true
          fail_on_unmatched_files: true
          prerelease: ${{ contains(github.ref_name, '-') }}
```

Notes:
- The binaries inside the published `.tar.gz` are codesigned; notarization
  registers them with Apple by hash. No re-packaging is needed after notarize.
- Linux/Windows archives are untouched (cross-built on the same macOS runner).
- Keep a `v*-rc*` prerelease run as your dry run before a public `v0.1.0`.

## 4. Verify

After a signed run, on a clean Mac:
```
curl -fsSL https://aoos.github.io/dejima/install-client.sh | bash
spctl -a -vvv -t install "$(command -v dejima)"   # should say: accepted, Developer ID
codesign -dv --verbose=4 "$(command -v dejima)"    # shows the Developer ID + hardened runtime
```
No Gatekeeper prompt = success. At that point the quarantine-strip in
`install-client.sh` is belt-and-suspenders, not load-bearing.

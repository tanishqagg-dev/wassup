# Releasing

Releases are built by GoReleaser in GitHub Actions when a `v*` tag is pushed.

Each release has:

- archives for macOS, Linux and Windows (amd64 and arm64), pure Go, no cgo
- `checksums.txt` with a keyless Sigstore signature (`checksums.txt.sigstore.json`)
- an SBOM per archive
- GitHub build-provenance attestations
- a Homebrew cask in `tanishqagg-dev/homebrew-tap`

## One-time setup

1. Create `github.com/tanishqagg-dev/wassup` and push this repo.
2. Create `github.com/tanishqagg-dev/homebrew-tap`.
3. Optional: a fine-grained token with contents read/write on `homebrew-tap` only, saved as the `HOMEBREW_TAP_TOKEN` secret, lets CI publish the cask itself. Without it CI skips the cask and you run `scripts/publish-cask.sh`.

## Cutting a release

```bash
git tag v0.1.0
git push origin v0.1.0
gh run watch                          # wait for the release workflow
scripts/publish-cask.sh v0.1.0        # only if HOMEBREW_TAP_TOKEN isn't set
```

## The Mac app

`macos/build.sh` builds the universal app with the CLI inside, signs it with the Developer ID identity and makes a DMG. With a notarytool keychain profile it also notarizes and staples:

```bash
xcrun notarytool store-credentials wassup-notary --apple-id <you> --team-id 422C63R2G8   # once
VERSION=0.1.0 NOTARY_PROFILE=wassup-notary ./macos/build.sh
cp macos/build/wassup-0.1.0.dmg wassup.dmg
gh release upload v0.1.0 wassup.dmg
```

The site's download buttons point at `releases/latest/download/wassup.dmg`, so the asset name must stay `wassup.dmg`. Then set `MAC_APP_READY = true` in `site/src/components/Download.tsx` and redeploy the site.

## Verifying a download

```bash
gh attestation verify wassup_0.1.0_darwin_arm64.tar.gz --repo tanishqagg-dev/wassup
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/tanishqagg-dev/wassup/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
shasum -a 256 -c checksums.txt --ignore-missing
```

The plain CLI binaries in the archives aren't notarized. The Homebrew cask removes the quarantine flag; a manually downloaded binary needs `xattr -d com.apple.quarantine wassup` once. The Mac app DMG is notarized when built with `NOTARY_PROFILE`.

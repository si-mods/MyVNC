# Releasing MyVNC

Workflow: `.github/workflows/ci.yml`. Push to `master` and PRs run gofmt, `go vet`
(`CGO_ENABLED=0`), `go test`, `go tool mygo build`, an installer test, and upload the
build as an artifact. A `v*` tag also creates a **pre-release** with
`MyVNC-Setup-<ver>-amd64.exe`, `MyVNC-<ver>-amd64.exe` and `SHA256SUMS.txt`.

## Cut a release

1. Bump `version` in `mygo.json`, commit, merge to `master`.
2. `git tag vX.Y.Z && git push origin vX.Y.Z`. The build fails if the tag differs from `mygo.json`.
3. Check the pre-release, then promote it by hand if wanted.

## Installer test

`.github/scripts/test-installer.ps1 -Installer <Setup.exe>` installs silently (`/S /D=<temp dir>`),
starts `MyVNC.exe`, checks it is alive after 3 s, kills it, uninstalls silently and checks that the
exe and the HKCU uninstall key are gone. It refuses to run if MyVNC is already installed for the
current user. CI runs it on every build. Upgrade-over-older-version is not covered yet (needs a
second released installer).

## Code signing (optional, off until configured)

Without secrets the build is unsigned (SmartScreen warns). mygo reads `windows.certificate` from
the config, so CI patches `mygo.json` at build time only when `WINDOWS_CERTIFICATE` exists; the
repo config stays unsigned for local builds. A missing certificate never fails the build.

PFX certificate:

```sh
base64 -w0 code-signing.pfx | gh secret set WINDOWS_CERTIFICATE -R kyya/MyVNC
gh secret set WINDOWS_CERTIFICATE_PASSWORD -R kyya/MyVNC
```

mygo then signs `MyVNC.exe`, the installer and the uninstaller with `signtool` (present on
`windows-latest`), and CI verifies each signature. Never commit the PFX.

Azure Trusted Signing (cloud key, no PFX): replace the "Enable code signing" step with
`azure/login` plus `azure/trusted-signing-action`-style setup that installs the dotnet signing
tool, and set `windows.signCommand` in the patched `mygo.json` to a command with `%1` as the file,
for example `signtool sign /v /fd sha256 /tr http://timestamp.acs.microsoft.com /td sha256 /dlib <Azure.CodeSigning.Dlib.dll> /dmdf <metadata.json> %1`.
`certificate` and `signCommand` exclude each other. Needs an Azure subscription, a validated
identity and an OIDC federated credential; details in mygo `docs/distribution.md` (Code signing).

## Auto-update: decision needed

mygo updates read signed manifests from a public GitHub repo (`updates.github`) or an HTTPS/S3
feed (`updates.url` / `updates.s3`). `kyya/MyVNC` is private, so updates cannot work from its
releases. Nothing is configured yet. Choose one:

- **A. Public release repo** (for example `kyya/MyVNC-releases`): CI uploads the release there; the
  source repo stays private. Needs a token secret with write access to that repo.
- **B. HTTPS/S3 feed** (for example an S3 bucket behind CloudFront, or Cloudflare R2): needs a bucket,
  its credentials as secrets, and a URL.
- **C. No auto-update**: users download new installers manually.

Either A or B also needs an update signing key, generated once by the owner:

```sh
go tool mygo keygen        # writes mygo-update.key (secret) and mygo-update.pub
gh secret set MYGO_UPDATER_PRIVATE_KEY -R kyya/MyVNC < mygo-update.key
```

Then add `updates.publicKey` (contents of `mygo-update.pub`) and `updates.github` or `updates.url`
to `mygo.json`, pass `MYGO_UPDATER_PRIVATE_KEY` to the build step, build with `-upload` (see mygo
`docs/updates.md` and `docs/github-actions.md`), and register `updater.Plugin` in the app. Keep the
secret key out of the repo; losing it strands installed copies.

# Releasing MyVNC

Workflow: `.github/workflows/ci.yml`. Push to `main` and PRs run gofmt, `go vet`
(`CGO_ENABLED=0`), `go test`, `go tool mygo build`, an installer test, and upload the
build as an artifact. A `v*` tag also publishes a release with
`MyVNC-Setup-<ver>-amd64.exe`, `MyVNC-<ver>-amd64.exe`, the update files and `SHA256SUMS.txt`.

## Cut a release

1. Bump `version` in `mygo.json` and add a `## <version>` section to `CHANGELOG.md`
   (the build fails without it). Commit, merge to `main`.
2. `git tag vX.Y.Z && git push origin vX.Y.Z`. The build fails if the tag differs from `mygo.json`,
   or if the update manifest was not produced.
3. CI creates the release as a draft, uploads all files, then publishes it as the latest.

## Self-hosted runner

`build` runs on `yibo-pc`, the desk machine (`D:\actions\myvnc`, labels `self-hosted`,
`Windows`, `X64`, `yibo-pc`), so pushes and same-repo PRs skip the `windows-latest`
queue. Fork PRs and `v*` tag builds stay on GitHub's runners: a fork's code must never
execute on a self-hosted machine, and the installer test — which installs and uninstalls
for the current user, so it refuses one who already has MyVNC, as `yibo-pc` does — runs
only on hosted runners.

The runner is started at logon from `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
(`wscript.exe D:\actions\myvnc\start-hidden.vbs`), so it runs while that user is logged
in. From an elevated prompt, `D:\actions\myvnc\svc.cmd install` turns it into a Windows
service that survives logoff. Re-register with a token from
`gh api -X POST repos/si-mods/MyVNC/actions/runners/registration-token`.

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
base64 -w0 code-signing.pfx | gh secret set WINDOWS_CERTIFICATE -R si-mods/MyVNC
gh secret set WINDOWS_CERTIFICATE_PASSWORD -R si-mods/MyVNC
```

mygo then signs `MyVNC.exe`, the installer and the uninstaller with `signtool` (present on
`windows-latest`), and CI verifies each signature. Never commit the PFX.

Azure Trusted Signing (cloud key, no PFX): replace the "Enable code signing" step with
`azure/login` plus `azure/trusted-signing-action`-style setup that installs the dotnet signing
tool, and set `windows.signCommand` in the patched `mygo.json` to a command with `%1` as the file,
for example `signtool sign /v /fd sha256 /tr http://timestamp.acs.microsoft.com /td sha256 /dlib <Azure.CodeSigning.Dlib.dll> /dmdf <metadata.json> %1`.
`certificate` and `signCommand` exclude each other. Needs an Azure subscription, a validated
identity and an OIDC federated credential; details in mygo `docs/distribution.md` (Code signing).

## Auto-update

Installed copies update themselves (native update window, daily check) from signed releases in
this repo (`updates.github: si-mods/MyVNC`).

- Tag builds sign with the `MYGO_UPDATER_PRIVATE_KEY` repo secret; other builds get no key and
  skip the update files. The public key is `updates.publicKey` in `mygo.json`.
- Next to the installers mygo writes `update-windows-amd64.json` (the manifest), the app archive
  `myvnc-<ver>-windows-amd64.tar.gz` and delta updates `myvnc-<old>-to-<ver>-windows-amd64.delta`.
  CI uploads them under these names and adds them to `SHA256SUMS.txt`.
- Deltas need the earlier release to be published; the first release has none.
- `CHANGELOG.md` must have a `## <version>` section for every release: it becomes the release notes
  in the update window.
- The release job creates a draft, uploads everything, then publishes it with `--latest`, so
  `releases/latest` never lacks the manifest.
- Losing the signing key strands installed copies (they accept only updates signed with it). The
  owner keeps a backup in a password manager; the local copy is in mygo's `update-keys` config dir.
- The first version with the updater (0.0.26) must be installed by hand; older copies cannot update.

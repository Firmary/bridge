**Beta and unsigned.** This is a pre-release build of the Firmary Helper for testing. It is not signed or notarized
yet, so your computer will warn before it runs. A signed build is coming.

**Pairing in Firmary is not open yet.** The relay's public listener stays closed until Firmary's pairing screen
ships. This build is ready to connect, but it cannot pair until then.

## Opening it despite the warning

- **macOS** (Gatekeeper says it can't check the app): in the folder you extracted, run
  `xattr -d com.apple.quarantine ./firmary-helper`, or right-click `firmary-helper` in Finder and choose **Open**.
- **Windows** (SmartScreen says "Windows protected your PC"): click **More info → Run anyway**.
- **Linux**: no warning.

## Check the download

Compare the archive's checksum with `SHA256SUMS`:

- macOS: `shasum -a 256 -c SHA256SUMS --ignore-missing`
- Linux: `sha256sum -c SHA256SUMS --ignore-missing`
- Windows (PowerShell): `Get-FileHash .\firmary-helper_<version>_windows_amd64.zip` and compare with the line in
  `SHA256SUMS`.

## Run it

In a terminal, in the extracted folder: `./firmary-helper run` (Windows: `.\firmary-helper.exe run`).
`./firmary-helper version` prints the version. The first run downloads the Helper's own pinned copy of Claude Code
(about 290 MB).

When pairing opens: the Helper shows a code; check that Firmary shows the same code and confirm there. The Helper
then names the person and firm and asks you to type `yes` on the computer. Anything else declines.

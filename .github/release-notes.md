**Beta and unsigned.** Firmary Link connects your own Claude or ChatGPT plan to Firmary. This build isn't signed by
Apple or Microsoft yet, so your computer warns the first time you open it. A signed build is coming.

## Install

- **Mac:** open `Firmary-Link.dmg` and drag **Firmary Link** to **Applications**, then open it. The first time, macOS
  blocks it: open **System Settings › Privacy & Security**, scroll down and click **Open Anyway**.
- **Windows:** run `Firmary-Link-Setup.exe`. If SmartScreen says "Windows protected your PC", click **More info ›
  Run anyway**.
- **Linux desktop:** install the `.deb` for your computer (`amd64` for most, `arm64` for Arm).
- **Linux server or VPS:** run this one line, then follow what it says:

  ```sh
  curl -fsSL https://github.com/Firmary/bridge/releases/latest/download/install.sh | sh
  ```

Once installed, Firmary Link updates itself quietly from this release's signed `manifest.json`.

## Files

| File                                                                                  | For                                                        |
| ------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| `Firmary-Link.dmg`                                                                    | Mac (Apple silicon and Intel)                              |
| `Firmary-Link-Setup.exe`                                                              | Windows 10 and 11 (x64 and Arm)                            |
| `firmary-link_<version>_amd64.deb`, `firmary-link_<version>_arm64.deb`                | Linux desktop (Debian, Ubuntu)                             |
| `install.sh`, `firmary-link-linux-x64.tar.gz`, `firmary-link-linux-arm64.tar.gz`      | Linux server; the tarballs are what `install.sh` downloads |
| `firmary-link-darwin-universal`, `firmary-link-linux-*`, `firmary-link-windows-*.exe` | The app on its own, used by updates                        |
| `manifest.json`, `manifest.json.sig`                                                  | The signed update manifest                                 |
| `checksums.txt`                                                                       | SHA-256 of every other file                                |

To check a download: `sha256sum -c checksums.txt --ignore-missing` (Mac: `shasum -a 256 -c checksums.txt
--ignore-missing`). `install.sh` checks its download for you.

**Beta and unsigned.** Firmary Bridge connects your own Claude or ChatGPT plan to Firmary. This build isn't signed by
Apple or Microsoft yet, so your computer warns the first time you open it. A signed build is coming.

**New in 0.3.3:** the server line works on any 64-bit Linux, with or without systemd: Docker containers, WSL and
small VPS images too. Where systemd doesn't run, Firmary Bridge pairs and then keeps running in the background, starts
again if it stops, and starts with the server when cron is there. After pairing, the line sends you back to Firmary
for the next step instead of printing more commands.

**0.3.2** renamed Firmary Link to **Firmary Bridge**. A computer that already has it updates by itself and stays
paired, signed in and set to open at login. On a server, the command is still `firmary-link`.

## Install

- **Mac:** open `Firmary-Bridge.dmg` and drag **Firmary Bridge** to **Applications**, then open it. The first time,
  macOS blocks it: open **System Settings › Privacy & Security**, scroll down and click **Open Anyway**.
- **Windows:** run `Firmary-Bridge-Setup.exe`. If SmartScreen says "Windows protected your PC", click **More info ›
  Run anyway**.
- **Linux desktop:** download `firmary-bridge_amd64.deb` (`firmary-bridge_arm64.deb` for Arm), then open it and
  choose **Install**, or run `sudo apt install ./firmary-bridge_*.deb` in its folder. It upgrades an installed
  `firmary-link` package in place.
- **Linux server or VPS:** in Firmary, open Settings › Connections › Add a server and paste the one line it shows
  (it carries your pairing code), then confirm the code in Firmary:

  ```sh
  curl -fsSL https://github.com/Firmary/bridge/releases/latest/download/install.sh | sh -s -- --code ABCD-EFGH
  ```

To pair the app on a Mac, Windows or Linux desktop, choose **Enter pairing code…** in its menu (it asks by itself the
first time) and type the code Firmary shows.

Once installed, Firmary Bridge updates itself quietly from this release's signed `manifest.json`.

## Files

| File                                                                                  | For                                                        |
| ------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| `Firmary-Bridge.dmg`                                                                  | Mac (Apple silicon and Intel), macOS 13 or later           |
| `Firmary-Bridge-Setup.exe`                                                            | Windows 10 and 11 (x64 and Arm)                            |
| `firmary-bridge_amd64.deb`, `firmary-bridge_arm64.deb`                                | Linux desktop (Debian, Ubuntu)                             |
| `firmary-bridge_<version>_amd64.deb`, `firmary-bridge_<version>_arm64.deb`            | The same two, with the version in the name                 |
| `firmary-link_amd64.deb`, `firmary-link_arm64.deb`                                    | The same two under their old names, for older links        |
| `install.sh`, `firmary-link-linux-x64.tar.gz`, `firmary-link-linux-arm64.tar.gz`      | Linux server; the tarballs are what `install.sh` downloads |
| `firmary-link-darwin-universal`, `firmary-link-linux-*`, `firmary-link-windows-*.exe` | The app on its own, used by updates                        |
| `manifest.json`, `manifest.json.sig`                                                  | The signed update manifest                                 |
| `checksums.txt`                                                                       | SHA-256 of every other file                                |

To check a download: `sha256sum -c checksums.txt --ignore-missing` (Mac: `shasum -a 256 -c checksums.txt
--ignore-missing`). `install.sh` checks its download for you.

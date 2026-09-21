# aNLpt

Share Dutch series and films from your own computer with [Archive NL](https://archive.my-dev.app),
without putting them on a Google Drive.

aNLpt is one small program for Windows and Linux. You sign in with your Archive NL account,
pick the folders you want to share, and leave it running in the background. It works out
which title and episode each file is, adds them to your library (which earns you XP), and
hands a file over when another member asks for it. Sharing is also what lets you download
what others share.

**Other members never see your computer.** Files travel through the archive: the member
who wants one asks the archive, the archive asks your aNLpt, and your aNLpt sends the file
to the archive, which passes it on. Nobody downloading learns your address, and you do not
learn theirs.

## Getting started

1. Download `aNLpt.exe` (Windows) or `anlpt-linux-amd64` (Linux) from the
   [releases page](../../releases).
2. Start it. An icon appears next to the clock and a page opens in your browser.
3. Click **Inloggen**, agree on the Archive NL page that opens, and come back.
4. Add the folder your series and films are in. That is all.

Anything the archive cannot recognise is listed on the page with a button that takes you to
Archive NL, where you pick the right title or add it to the catalogue.

### Windows says "Windows protected your PC"

aNLpt is not signed with a paid certificate, so SmartScreen warns the first time you start a
new version. Click **More info** and then **Run anyway**. Before you do, you can check that
the file is the one we published: every release lists a SHA-256 checksum, and
`Get-FileHash .\aNLpt.exe` in PowerShell prints yours.

### On a server without a screen

```sh
./anlpt-linux-amd64 --headless
```

It prints an address to sign in at; open it in a browser on the same machine (or through an
SSH tunnel). To keep it running, a systemd user unit does the job:

```ini
# ~/.config/systemd/user/anlpt.service
[Unit]
Description=aNLpt
After=network-online.target

[Service]
ExecStart=%h/bin/anlpt-linux-amd64 --headless
Restart=on-failure

[Install]
WantedBy=default.target
```

```sh
systemctl --user enable --now anlpt
loginctl enable-linger "$USER"   # keep it running while you are logged out
```

## What it sends, and what it does not

The source is public so that you can check this, and it is all in one file:
[`internal/api/api.go`](internal/api/api.go).

| Sent to the archive | Never sent |
| --- | --- |
| The **name** of a shared folder | Where that folder is on your computer |
| Names, sizes and dates of the **video files** inside it | Any other file. Subtitles, pictures and documents are never even listed |
| For each of those files, **who may download it** and **what it is spoken in**, where you set that for a series, a season or an episode | Which folders or files you set it on: aNLpt works it out here and only says the outcome per file |
| A video file, when a member asks for it | Anything outside the folders you chose |
| The name of your computer, your operating system, the aNLpt version | Anything else about your computer |

How it is kept that way:

- **The archive cannot name a path.** It asks for a file by an id that aNLpt made up while
  scanning your shared folders. An id that is not in that list is refused, so there is no
  request that reads a file you did not share ([`internal/scan`](internal/scan/scan.go)).
- **"Nobody" means nobody at once.** A folder, series, season or episode you keep back is
  refused by aNLpt itself from the moment you choose it, whether or not the archive has
  heard yet.
- **Links out of a shared folder are not followed**, hidden files are skipped, and a whole
  disk or your whole home folder cannot be shared by accident.
- **No password is typed into aNLpt.** You sign in on the archive's own page in your own
  browser (OAuth 2 with PKCE). The program holds a token that is only good for sharing; it
  cannot read your messages, change your profile or do anything else as you. The token is
  kept in the Windows Credential Manager or the Linux Secret Service.
- **Its own page is for you only.** It listens on `127.0.0.1` and needs a token that is
  made up each time the program starts.
- **No telemetry, no advertising, no automatic updates.**

You can sign any computer out from Archive NL under **Wat ik deel**, and whoever runs the
archive can switch a client off.

## Settings

Everything is on the page: per folder who may see it, whether your name is on it, the
spoken language and whether others may download; and for the program whether it starts
with your computer, how often folders are looked through, and a cap on upload speed.

Files are kept in `%AppData%\aNLpt` on Windows and `~/.config/aNLpt` on Linux:
`config.json` (no secrets), `anlpt.log`.

## Building it yourself

Go 1.26 or newer, and nothing else. No C compiler is needed.

```sh
go test ./...
go build -trimpath -ldflags "-s -w" -o anlpt ./cmd/anlpt                      # Linux
GOOS=windows go build -trimpath -ldflags "-s -w -H=windowsgui" -o aNLpt.exe ./cmd/anlpt
```

To run against a development copy of the archive: `anlpt --server http://localhost:53000`.

Releases are built by GitHub Actions from a tag ([`.github/workflows/release.yml`](.github/workflows/release.yml)),
with no packer and no obfuscation, so what you download can be compared with what you build.

## Security

See [SECURITY.md](SECURITY.md).

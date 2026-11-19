# Laptop setup

One-time steps that connect a laptop to a devbox. At the end, the developer
opens a terminal or their editor and is on the devbox, sees a dev server from
the devbox in the browser, and gets an alert when Claude Code needs them.

Before starting, the operator has created and provisioned the devbox (see
[operator-guide.md](operator-guide.md)) and given the developer:

- the devbox's name on the tailnet (default `devbox`);
- their Linux user name on it (shown as `<user>` below);
- the apps domain (default `devbox`, as in `http://shop.devbox`).

Commands are given for Windows (PowerShell) and macOS. On Linux, follow the
macOS steps for SSH and the editor; the differences are noted.

## 1. Tailscale

Install Tailscale and log in with the account the devbox is on.

- **Windows:** `winget install --id Tailscale.Tailscale -e`, then open
  Tailscale from the tray and log in.
- **macOS:** install from https://tailscale.com/download or the Mac App
  Store, then log in from the menu bar.
- **Linux:** `curl -fsSL https://tailscale.com/install.sh | sh`, then
  `sudo tailscale up`.

Check that the devbox shows up in Tailscale's machine list and that
`ping devbox` gets replies.

### Check the project names

`<anything>.devbox` must resolve to the devbox. This depends on the split DNS
entry the operator adds in the Tailscale admin console.

| OS | Command | Note |
|---|---|---|
| Windows | `Resolve-DnsName shop.devbox` | Not `nslookup`: it ignores Tailscale's split DNS on Windows |
| macOS | `dscacheutil -q host -a name shop.devbox` | Not `dig` or `nslookup`: they bypass the system resolver |
| Linux | `getent hosts shop.devbox` | |

The answer is the devbox's Tailscale address (`100.x.y.z`). Any name works,
even one with no project behind it. Then open `http://devbox/` in a browser;
type the trailing `/` the first time so the browser doesn't search for it.

## 2. Font (optional)

The devbox prompt uses symbols from a Nerd Font.

- **Windows:** `winget install --id DEVCOM.JetBrainsMonoNerdFont -e`. If winget
  can't find it, download JetBrainsMono from https://www.nerdfonts.com/font-downloads,
  unzip it, select all the `.ttf` files, right-click, Install.
- **macOS:** `brew install --cask font-jetbrains-mono-nerd-font`, or the same
  download as above.

## 3. SSH config

The devbox accepts SSH over Tailscale (Tailscale SSH), so no key is needed.
Add a host entry so `ssh devbox` logs in as the right user.

Windows (PowerShell; appends to an existing file):

```powershell
New-Item -ItemType Directory -Force "$env:USERPROFILE\.ssh" | Out-Null
Add-Content -Encoding ascii "$env:USERPROFILE\.ssh\config" "`nHost devbox`n  HostName devbox`n  User <user>`n"
ssh devbox
```

macOS and Linux:

```sh
mkdir -p ~/.ssh
printf '\nHost devbox\n  HostName devbox\n  User <user>\n' >> ~/.ssh/config
ssh devbox
```

The prompt shows `devbox ~`. Type `exit` to leave.

## 4. Terminal profile

A profile that opens the tmux workspace on the devbox. Closing the tab and
opening it again returns to the same session, with everything still running.

- **Windows Terminal:** Settings → Add a new profile → New empty profile.
  Name `devbox`; Command line `ssh -t devbox "tmux new -A -s main"`;
  Appearance → Font face `JetBrainsMono Nerd Font`. Optionally make it the
  default profile.
- **macOS and Linux:** make a profile in the terminal app that runs the same
  command, or run `ssh -t devbox "tmux new -A -s main"` (on the devbox, `t`
  does the same).

## 5. Editor: Cursor or VS Code

- Install the **Remote - SSH** extension.
- Command palette → **Remote-SSH: Connect to Host** → `devbox`. Platform:
  **Linux**.
- **Open Folder** → `/home/<user>/projects`.
- Optional, in settings JSON: `"terminal.integrated.fontFamily": "JetBrainsMono Nerd Font"`.

The status bar shows `SSH: devbox`, and the editor's terminal is on the devbox.

## 6. Trust the devbox certificate

Project links are `https://<project>.devbox`, signed by the devbox's own
certificate authority. Trust it once per laptop. `ds cert` on the devbox
prints these steps too.

Windows (PowerShell, not as administrator; confirm the dialog):

```powershell
Invoke-WebRequest http://devbox/root.crt -OutFile devbox-root.crt
Import-Certificate -FilePath devbox-root.crt -CertStoreLocation Cert:\CurrentUser\Root
```

macOS:

```sh
curl -o devbox-root.crt http://devbox/root.crt
security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db devbox-root.crt
```

Restart Chrome or Edge afterwards. Firefox keeps its own list: Settings →
Privacy & Security → Certificates → View Certificates → Authorities → Import →
`devbox-root.crt` → trust it to identify websites.

On Linux, add the certificate to the browser's certificate store the same way.

Check: `https://devbox/` opens without a warning. Phones and other devices
without the certificate keep using `http://<project>.devbox`.

## 7. Logins on the devbox

In a devbox terminal. Browser logins that redirect to `localhost` don't work
over SSH, so use device codes or tokens.

```sh
gh auth login            # GitHub.com → HTTPS → Login with a web browser:
                         #   open the URL on the laptop and enter the code
gh auth setup-git        # git push and pull to GitHub use the gh login
git config --global user.name  "Your Name"
git config --global user.email "you@example.com"
git config --global init.defaultBranch main
claude                   # log in: open the URL on the laptop, paste the code back
```

With `DEVBOX_FORGE=gitlab` (`ds config` shows the setting), also log in to
GitLab. First create a personal access token in GitLab (Preferences → Access
tokens) with the scopes `api` and `write_repository`, then:

```sh
glab auth login          # gitlab.com → Token → paste it → HTTPS →
                         #   authenticate Git with the GitLab credentials: yes
```

Then run `ds doctor`; it reports any login that is still missing.

## 8. First project

```sh
ds new nextjs hello
```

It ends with the project's link, `https://hello.devbox`. Open it on the
laptop, then open `~/projects/hello` in the editor, change a page and watch it
reload. `ds` lists projects and links; `ds stop hello` stops it.

## 9. Screenshots for Claude Code

Claude Code on the devbox can't see the laptop's clipboard. A helper uploads
the clipboard image to the devbox (`~/.devbox/inbox/`) and copies its full
path; paste that path into Claude. `ds inbox` on the devbox lists what
arrived; files are deleted after 7 days. Both helpers need `ssh devbox` to
work.

**Windows.** In PowerShell:

```powershell
cd $env:TEMP
scp devbox:.devbox/src/laptop/windows/install.ps1 .
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

This puts `devbox-paste.ps1` in `%USERPROFILE%\devbox\` and adds a Start-menu
shortcut "devbox paste" with the hotkey **Ctrl+Alt+V**. Take a screenshot with
Win+Shift+S, press Ctrl+Alt+V (a notification says the path was copied), then
Ctrl+V in Claude. If the hotkey does nothing, sign out of Windows and back in
once.

**macOS.**

```sh
mkdir -p ~/.local/bin
scp devbox:.devbox/src/laptop/mac/devbox-paste.sh ~/.local/bin/
chmod +x ~/.local/bin/devbox-paste.sh
brew install pngpaste    # optional; without it the script uses osascript
```

Take a screenshot to the clipboard (Cmd+Ctrl+Shift+4), run
`devbox-paste.sh`, then Cmd+V in Claude. `devbox-paste.sh shot.png` sends a
file instead. To bind it to a key, create a shortcut in the Shortcuts app with
a "Run Shell Script" action that runs the script, and give it a keyboard
shortcut; it then reports through Notification Center.

## 10. Alerts: bell and phone

**Bell.** When Claude Code finishes or waits for input, the devbox terminal
rings, from any tmux window. Make the terminal show it:

- **Windows Terminal:** Settings → `devbox` profile → Advanced → Bell
  notification style: tick **Flash taskbar** (untick **Audible** for silence).
  The taskbar button flashes while Terminal is in the background and the tab
  shows a bell.
- **macOS:** in the terminal app's profile settings, turn on a visual bell or
  a Dock badge or bounce for the bell.
- **Cursor or VS Code terminal:** Settings → search `terminal bell` → turn on
  the visual bell.

**Phone (optional).** On the devbox, run `ds notify setup`. Install the free
ntfy app (no account), tap **+** and enter the topic it prints, or scan the QR
code. The test push arrives within seconds. A push contains the project name
and a fixed text, never code or prompts. `ds notify off` stops pushes. Tailscale
on the phone also makes the project links work there.

## 11. Moving work over

- Projects in git: push any uncommitted work from the laptop, then on the
  devbox: `cd ~/projects && gh repo clone <owner>/<repo>`.
- Folders not in git: drag them into the editor's file explorer while it's
  connected to the devbox, without `node_modules`, and install dependencies on
  the devbox.
- Docker Desktop on the laptop is no longer needed; quitting it and turning off
  its start at login often frees several GB of RAM.

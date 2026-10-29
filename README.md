# jkvm

A command-line client for [JetKVM](https://jetkvm.com). It takes screenshots, types text, presses key
combinations, moves and clicks the mouse, mounts virtual media and calls the device's JSON-RPC API, from a
shell or a script, with no browser.

jkvm talks to the device the way its own web UI does: WebSocket signalling, then a WebRTC session (with
[pion](https://github.com/pion/webrtc)) carrying the `rpc` JSON-RPC data channel, the binary `hidrpc` channel
and the H.264 video track. [PROTOCOL.md](PROTOCOL.md) has the wire details.

> jkvm is an unofficial, independent project. It is not affiliated with, endorsed by or supported by JetKVM.
> "JetKVM" is used only to say which device it works with.

## Why

A KVM shows the machine before any OS is running: the BIOS, boot menus, installers, Secure Boot prompts.
SSH and RDP can't reach any of that. jkvm makes each of those steps a shell command that a script or an AI
agent can run and then check with a screenshot. `snapshot` shows the screen, `key` and `type` act on it,
and `media mount-file` supplies the install media.

[docs/example-proxmox-dual-boot.md](docs/example-proxmox-dual-boot.md) walks through a real use of it: an
agent turned a Windows desktop into a Windows + Proxmox dual-boot machine through the BIOS, a memory test,
the OS install and the boot setup, without anyone touching the PC.

## Quick start

You need Go 1.26 or newer to build it, `ffmpeg` in `PATH` for `snapshot` and `wait-screen`, and a JetKVM
on firmware 0.5.9 or later that your machine can reach directly (see [Limitations](#limitations)).

```sh
go install github.com/tfyl/jkvm/cmd/jkvm@latest

export JKVM_HOST=http://jetkvm.local    # or the device's IP address
export JKVM_PASSWORD=...                # only if the device has a local password

jkvm info
jkvm snapshot -o screen.png
jkvm key win+r
jkvm type -e 'notepad\n'
```

Prebuilt binaries for Linux and macOS are on the [releases page](https://github.com/tfyl/jkvm/releases).

## How it works

Each command connects, does its job and disconnects. A connection takes about 0.4 s on a LAN (1.2 s for a
snapshot), so there is no daemon.

The device allows one session at a time: a new one kicks the previous one, including a browser tab on the
web UI. jkvm serialises its own commands per device with an `flock` on `$TMPDIR/jkvm-<host>.lock`, so
parallel invocations queue instead of kicking each other.

When a session closes, the device releases every key and cancels any running keyboard macro. `type` and
`key` therefore send their input as a device-side keyboard macro (the way the web UI pastes) and wait for
the device to report the macro finished before disconnecting. Timing doesn't depend on the network.

## Usage

```
jkvm <command> [args] [--host URL] [--password PW] [--timeout 60s] [-v]
```

Flags can go before or after the command. `jkvm <command> --help` shows each command's flags and
examples, and `jkvm completion bash|zsh|fish` prints a shell completion script.

| flag | env | default | |
| --- | --- | --- | --- |
| `--host` | `JKVM_HOST` | (required) | the device, e.g. `http://jetkvm.local`. `http://` is added if missing |
| `--password` | `JKVM_PASSWORD` | (empty) | the local password; leave empty when the device has none |
| `--timeout` | | `60s` | limit for the whole command |
| `-v`, `--verbose` | | off | log signalling, ICE candidates and connection state to stderr |

Exit codes: 0 success, 1 failure, 2 usage error.

| command | what it does |
| --- | --- |
| `jkvm info` | device id, firmware, video state, USB state and devices, keyboard layout and LEDs, mounted media (JSON) |
| `jkvm snapshot -o f.png` | requests a keyframe, decodes it and writes a PNG at the native resolution. Fails fast when there is no HDMI signal |
| `jkvm wait-screen -o f.png [-t/--wait 60s] [--interval 1s] [--threshold 0.01]` | polls frames until more than `--threshold` of the screen changes, then saves that frame. Exits 1 on timeout. Raise `--timeout` too for waits over 60s |
| `jkvm type [-l/--layout en-UK] [-d/--delay 20] [-e] 'text'` | types text. Every character is checked against the layout before anything is sent; one it can't type is a usage error (exit 2) |
| `jkvm key [-l/--layout L] [--hold 50] combo...` | presses key combinations in turn, e.g. `win+x`, `ctrl+alt+Delete`, `enter`, `f11` |
| `jkvm mouse move [-n/--norm] X Y` | absolute move in screen pixels, or 0..1 fractions with `--norm` |
| `jkvm mouse click [-n/--norm] [-b/--button left\|right\|middle] [-c/--count 2] X Y` | move, press, release |
| `jkvm mouse scroll DY` | wheel detents; positive scrolls up, negative down |
| `jkvm media status` | mounted image, storage space, stored files, mass-storage mode |
| `jkvm media mount-url [-m/--mode CDROM\|Disk] URL` | has the device stream an image from an HTTP(S) server that supports range requests |
| `jkvm media mount-file [-m/--mode CDROM\|Disk] PATH` | uploads an image to device storage and mounts it |
| `jkvm media unmount` | ejects the mounted image |
| `jkvm rpc METHOD ['{"json":"params"}']` | raw JSON-RPC call, e.g. `jkvm rpc getATXState` |
| `jkvm display [status]` | video mode (size, fps, ready), the EDID in use and the name of its preset |
| `jkvm display presets` | the device's EDID presets and their resolutions |
| `jkvm display set [-s/--save prev.edid] PRESET\|HEX` | sets the EDID, waits for video to come back and prints the new mode and the previous EDID |
| `jkvm display reset` | sets the `JetKVM Default` EDID preset |
| `jkvm events [-t/--duration 10s] [--video]` | prints device notifications (`videoInputState`, `usbState`, ...) as JSON lines |
| `jkvm version` | prints the version (also `jkvm --version`) |
| `jkvm completion bash\|zsh\|fish` | prints a shell completion script |

### Typing text

`type` types its argument exactly. A real newline in the argument presses Enter and a tab presses Tab, so
in a shell you can write:

```sh
jkvm type $'first line\nsecond line\n'
```

With `-e` (`--escapes`), the escapes `\n` (Enter), `\t` (Tab) and `\\` (a backslash) are expanded first. Any other
backslash stays as it is, so `C:\Users` types as written; with `-e`, write `C:\new` as `C:\\new`.

```sh
jkvm type -e 'Get-ChildItem C:\Users\n'
jkvm type -- '-text that starts with a dash'
```

`--delay` is the pause between characters in milliseconds (default 20, like the web UI's paste).

### Key combinations

A combo is keys joined with `+`. Modifiers are pressed first, then the keys, then everything is released.
Each combo argument is pressed in turn.

- Modifiers: `ctrl`, `shift`, `alt`, `altgr`, `win` (also `super`, `meta`, `cmd`), and right-hand variants
  `rctrl`, `rshift`, `ralt`, `rwin`.
- Named keys: `enter`, `esc`, `tab`, `space`, `backspace`, `delete`, `insert`, `home`, `end`, `pageup`,
  `pagedown`, `up`, `down`, `left`, `right`, `f1`..`f24`, `printscreen`, `menu`, `capslock`, keypad keys
  `kp0`..`kp9`, `kpenter`, and more. Names are case-insensitive.
- A single character means its key on the layout: `ctrl+z`, `ctrl++`, `win+.`.

```sh
jkvm key ctrl+alt+Delete
jkvm key win+r && jkvm type -e 'cmd\n'
jkvm key --hold 3000 f12    # hold F12 for 3 s, e.g. for a boot menu
```

### Keyboard layout

A USB keyboard sends key positions, and the target's operating system decides which character each one
produces. jkvm needs to know that layout to type symbols correctly. `type` and character keys in `key` use
`--layout`, else `$JKVM_LAYOUT`, else the layout stored on the device (`getKeyboardLayout`).

Supported: `en-US` and `en-UK` (also `us`, `uk`, `gb`, `en-GB`), with tables taken from the firmware's UI.
If symbols come out wrong (for example `"` and `@` swapped), the layout doesn't match the target's.

### Mouse

`mouse move` and `mouse click` use the absolute pointer. Pixel coordinates are converted with the
resolution the device reports for the HDMI input; use `--norm` with 0..1 fractions when there is no signal.
Negative numbers are arguments, not flags, so `mouse scroll -3` scrolls down.

```sh
jkvm mouse click 960 540
jkvm mouse click --norm --button right 0.5 0.5
jkvm mouse scroll -3
```

### Virtual media

```sh
jkvm media mount-url https://example.com/debian.iso
jkvm media mount-file --mode Disk ./memtest.img
jkvm media status
jkvm media unmount
```

`mount-url` asks the device to check the URL first (`checkMountUrl`), so an unreachable or unusable URL
fails up front with the device's reason. `mount-file` uploads the image to the device's storage (about
14 GB), resumes an interrupted upload, and skips the upload when a file with the same name and size is
already there. A file with the same name and a different size is an error; delete it with
`jkvm rpc deleteStorageFile '{"filename":"NAME"}'`. Only one image can be mounted at a time.

### Display and EDID

The JetKVM presents an EDID to the target over HDMI, and the target picks its resolutions from it. The
device has presets (`jkvm display presets`); `display set` takes a preset name or raw EDID hex. Save the
current EDID first so you can put it back:

```sh
jkvm display                                              # mode, EDID and preset in use
jkvm display set --save prev.edid 'Acer B246WL, 1920x1200'
jkvm snapshot -o s.png                                    # now 1920x1200
jkvm display set "$(cat prev.edid)"                       # back to what it was
jkvm display reset                                        # or the JetKVM Default preset
```

`set` waits up to `--timeout` for the device to report video again and prints the new mode, with the old
EDID as `previous`. The target may take a few seconds to switch.

### Raw RPC and events

`rpc` calls any JSON-RPC method and prints the result. [PROTOCOL.md](PROTOCOL.md) lists the methods. Some of
them change the device (network settings, updates, factory reset), so check before calling one you don't
know. For ATX power control, if the ATX extension is attached:

```sh
jkvm rpc setATXPowerAction '{"action":"power-short"}'
```

`events` keeps a session open and prints notifications as JSON lines for `-t`/`--duration`. `--video` also starts the
video stream, which makes the device report video state changes.

## Limitations

- jkvm connects over the LAN only. It uses no STUN or TURN servers (the local web UI doesn't either), and the
  device offers only its LAN address as an ICE candidate, so the machine running jkvm must reach the device
  directly. JetKVM Cloud is not supported.
- Firmware 0.5.9 removed the old `POST /webrtc/session` signalling. jkvm uses the WebSocket signalling and
  needs 0.5.9 or later.
- Running jkvm disconnects anyone using the web UI at that moment, and the other way round.
- Some BIOSes and boot loaders ignore the absolute mouse. Drive them with `key`.
- Only `en-US` and `en-UK` layouts are built in.
- Linux and macOS only: the per-device lock uses `flock`.
- The video track is H.264 only (jkvm doesn't offer H.265), decoded by the system `ffmpeg`.

### macOS targets

- jkvm's `en-UK` is the PC UK layout. Apple's "British" layout matches `en-US` except for `#` (shift+3 gives
  `£`), so with `--layout en-UK` the pairs `@`/`"`, `#`/`\` and `~`/`|` come out swapped on a Mac. Use
  `en-US` for a Mac.
- macOS treats the JetKVM as a display. In extended-desktop mode the absolute mouse maps onto the main
  display, not the JetKVM's screen, so clicks land in the wrong place. Mirror the displays to click. Changing
  the EDID can make macOS drop mirroring and go back to extended.
- Some macOS system dialogs ignore absolute clicks; the screen-sharing picker ("What do you want to show on
  JetKVM") did in testing. Use the keyboard there.

## Troubleshooting

- **Connection times out.** Run with `-v` to see ICE progress. The usual cause is a network path that isn't
  direct: a VPN, a different VLAN or subnet, or a firewall blocking UDP between the machine and the device.
- **The first attempt fails right after another session.** The device sometimes closes the signalling
  socket just after a previous session ended. jkvm retries up to three times.
- **`no keyframe from the device`.** `snapshot` reconnects once on its own. If it keeps happening, check the
  video in the web UI.
- **The browser UI says "Connecting to device"** while jkvm works: a tab still running a pre-0.5.9 UI, a
  browser that restricts WebRTC to proxied UDP (some privacy settings and VPN extensions do), or another
  client (such as jkvm) holding the session.

## Architecture

```
cmd/jkvm            flags, commands, output
internal/session    WebSocket signalling, the pion PeerConnection, JSON-RPC and hidrpc channels
internal/kvm        the interfaces the feature packages use (kvmtest: a fake device for tests)
internal/hid        keyboard macros, key combos, layouts, mouse
internal/video      H.264 depacketising, ffmpeg decode, screen-change detection
internal/media      virtual media and device info
internal/display    EDID presets and the video mode
e2e                 end-to-end tests that run the binary against a real device
```

## Compatibility

Tested with firmware 0.5.9 (app) / 0.2.8 (system) against Windows 11 and Linux targets at 1920x1080. Written
against the device's UI bundle and the official firmware source,
[jetkvm/kvm](https://github.com/jetkvm/kvm) at `release/0.5.9`.

## Development

```sh
make check   # everything CI runs: gofmt, go vet, go test -race, golangci-lint, govulncheck
make test    # gofmt check, go vet, go test -race
make lint    # golangci-lint
make build   # ./bin/jkvm
make e2e     # end-to-end tests against a real device at JKVM_E2E_HOST
make snapshot  # release archives in dist/, nothing published
```

The video tests need `ffmpeg` and are skipped without it. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Acknowledgements

[shvartzj1/jetkvm-mcp](https://github.com/shvartzj1/jetkvm-mcp) (MIT) was read as a reference and
cross-checked against the firmware source; no code was copied from it.

## License

MIT. See [LICENSE](LICENSE).

# Changelog

## 0.1.0 (2026-10-29)

First public release.

- `info`, `rpc` and `events`: device state, raw JSON-RPC calls and notifications.
- `snapshot` and `wait-screen`: PNG screenshots decoded from the H.264 stream with `ffmpeg`, and waiting for
  the screen to change.
- `type`: text through the device-side keyboard macro, checked against the `en-US` or `en-UK` layout before
  anything is sent. `-e` expands `\n`, `\t` and `\\`.
- `key`: key combinations such as `ctrl+alt+Delete`, `win+x` and `f11`, with modifiers pressed first.
- `mouse move`, `click` and `scroll`: absolute pointer in pixels or 0..1 fractions, and the wheel.
- `media`: mount an image from a URL or upload one to device storage (resumable), status and unmount.
- One session per device at a time, serialised with a lock file; signalling retries on a device that
  closes the socket right after a previous session.
- `display`: the video mode, the EDID in use, EDID presets, and `display set` / `display reset` to change the
  EDID, saving the previous one.
- `--host` (or `JKVM_HOST`) is required; there is no default device address.
- GNU-style flags (`--host`, `-o/--output`, ...) that can go before or after the command, `--help` with
  examples for every command, `--version`, and `jkvm completion bash|zsh|fish`. Release archives include the
  completion scripts.
- Exit codes: 0 success, 1 failure, 2 usage error. A character the layout can't type is a usage error.

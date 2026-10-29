# Contributing

Issues and pull requests are welcome.

- `make check` (gofmt, vet, tests, golangci-lint, govulncheck) must pass.
- New behaviour needs a test. `internal/kvm/kvmtest` is a fake device to test commands against without
  hardware.
- jkvm is used by scripts, so keep flags, output and exit codes stable. If one has to change, say so in the
  pull request and in CHANGELOG.md.
- If you change how jkvm talks to the device, say which firmware version you checked it against, and update
  PROTOCOL.md. Point to the firmware source where you can.
- Don't include device IDs, IP addresses, passwords or screenshots of your own machines in issues, tests or
  docs.
- Add a dependency only when it clearly beats a few lines of our own, and say why in the pull request.
- Use conventional commit messages (`fix: ...`, `feat: ...`) and add a line to CHANGELOG.md.

## End-to-end tests

`e2e/` builds jkvm and runs it against a real JetKVM. CI never runs it, and it skips without
`JKVM_E2E_HOST`:

```sh
JKVM_E2E_HOST=http://jetkvm.local make e2e     # JKVM_E2E_PASSWORD if the device has one
```

The default tier only reads: `version`, `info`, `rpc`, `media status`, `display`, `events`, `snapshot`,
`wait-screen` timing out, and error exit codes. One case connects and asks `type` for a character the layout
can't type, which is rejected before anything is sent.

`JKVM_E2E_INPUT=1` adds a tier that acts on the target machine: it moves the mouse, left-clicks the centre
of the screen, scrolls, presses shift, types `jkvm e2e` and deletes it with backspace, uploads and mounts a
1 MiB disk image (then unmounts and deletes it), and switches the EDID to a 1920x1200 preset and back. Run
it only on a machine you own, with nothing open that a click or typed text could harm. It checks only that
each command succeeds and saves a snapshot after each step in `JKVM_E2E_OUT` (else a temp directory it logs)
for you to look at.

```sh
JKVM_E2E_HOST=http://jetkvm.local JKVM_E2E_INPUT=1 JKVM_E2E_OUT=./e2e-out make e2e
```

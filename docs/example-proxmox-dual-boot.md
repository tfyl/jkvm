# Example: turning a Windows desktop into a dual-boot Proxmox node

This is a real job that jkvm was built for. An AI coding agent, running on a Linux box on the same LAN,
turned a Windows 11 desktop into a Windows + Proxmox VE dual-boot machine. It did the whole thing from
start to finish. Nobody sat at the PC: the agent did everything through the JetKVM plugged into it, and
the owner only approved the steps that wrote to disk or changed firmware settings.

Some machine-specific details are left out. The steps and the problems are as they happened.

## Why this wasn't a simple install

The Proxmox installer assumes a dedicated disk: it wipes the whole drive. This PC was someone's everyday
Windows machine with one NVMe disk, and Windows had to come through unharmed. Every requirement below
made the job harder:

- **Windows stays, on the same disk.** That rules out the Proxmox ISO. Instead the route is Debian 12,
  partitioned by hand into the free space, then converted to Proxmox VE 8.
- **Shrinking `C:` while Windows is running.** Windows won't move some files while it's running. The
  shrink stopped several times, on the pagefile, hibernation, restore points and finally the NTFS MFT
  bitmap. Each one had to be cleared and the shrink retried, and it ended at about 790 GB.
- **Secure Boot stays on.** Games on the Windows side need it, so every part of the boot chain had to be
  signed: shim, GRUB, the Proxmox kernel and the NVIDIA DKMS module. That last one needs a Machine Owner
  Key enrolled at a firmware screen that appears for about 10 seconds.
- **The firmware works against you.** The board reset the EFI boot order on every POST and ignored
  `efibootmgr`, so the boot order had to be set in the BIOS, with the keyboard only.
- **The installer couldn't see its own install media.** On the first boot the virtual CD never showed up.
  The agent found the cause from a shell inside the installer (a stuck USB storage scan) and fixed it with
  a clean reboot. Nothing had been written to the disk yet.
- **The node joins a live cluster without breaking it.** The PC is off the cluster whenever Windows runs, so
  it joins with zero quorum votes. `pvecm add --votes 0` silently ignored the zero and gave the node a
  vote, which would have cost the cluster quorum when the PC booted into Windows. The agent spotted this and
  fixed `corosync.conf` by hand.
- **The hardware has its own limits.** The 10 GbE card's VLAN table overflows with VLAN-aware bridging
  at the full 2–4094 range, so the bridge got a narrower range. LVM sized the thin pool's metadata far too
  small for 620 GB, so it was grown. RAM was moved to its XMP profile and tested before anything relied on it.
- **The GPU stack.** The workload needs NVIDIA's 580-series open driver, pinned so routine upgrades
  don't replace it, built through DKMS and signed with the enrolled key.

Each of these is a point where a hand-run install usually stalls, while you search for the cause on another
machine with the PC stuck on a firmware screen.

## The time it took

The times below count only the time an agent was driving the PC through jkvm. They come from the
timestamps of its jkvm calls, and any gap longer than 10 minutes (waiting for a defrag to finish, or
for the owner) is left out:

| stage | jkvm calls | time driving jkvm |
| --- | --- | --- |
| survey the machine and prepare Windows | 56 | ~50 min |
| shrink `C:` around the files Windows can't move | 78 | ~1 h 10 min |
| BIOS + XMP, memory test, Debian install, Proxmox conversion, boot order | 252 | ~2 h 30 min |
| cluster join, NVIDIA driver and Secure Boot key | 26 | ~35 min |
| **total** | **412** | **about 5 hours** |

Including the waits, it all ran overnight between one evening and the next morning.

The owner's part was approving the risky steps (the partition write, the BIOS save, removing the stock
kernels, joining the cluster), answering a handful of questions (partition sizes, whether to enable XMP,
how long to run the memory test) and two physical tasks: uninstalling some apps to free disk space and
setting a fixed IP on the router. That came to well under an hour of attention, mostly spent asking how it
was going.

Doing it by hand is harder to measure, but even for someone comfortable with Linux, the blockers above
add up to a weekend: researching the Debian-to-Proxmox route and Secure Boot signing, working around each
shrink blocker, recovering from the missing install media, and finding the quorum-vote bug, which only
shows when the node goes offline. jkvm doesn't make any single step faster. It means a person doesn't have
to sit at the machine for five hours of console work, and every step leaves a screenshot that can be checked.

jkvm itself improved during the job: the `mount-url` usability check, scrolling down, and `type -e` were all
fixed in the middle of it, when the work first ran into them.

## Why a CLI and not the web UI

The JetKVM web UI works well for a person. An agent, or a script, needs something else:

- **Each step is one shell command.** `jkvm snapshot` returns a PNG the agent can look at, and `jkvm key`
  and `jkvm type` return when the device has finished typing. Each action leads straight to a screenshot
  that checks it, which is how an agent works anyway.
- **It works before any OS is up.** SSH and RDP need a running system with a network. The KVM sees the
  BIOS, the boot menu, the installer and the Secure Boot screens, which is where a dual-boot install
  spends most of its time.
- **Install media without USB sticks.** `media mount-file` uploads an ISO or disk image to the device and
  presents it to the PC as a USB CD or disk, so nobody has to plug anything in.
- **Results can be checked.** Each step ends with a screenshot, so the agent can check the result before it
  does the next thing, and a person can look at the same image before they approve a risky step.

## How it went

### 1. Prepare Windows (keyboard and screenshots only)

Windows was running, so the agent opened an admin PowerShell with `win+x`, typed commands with
`jkvm type`, and read the output from snapshots. It:

- recorded the hardware, disk layout, BitLocker and Secure Boot state;
- turned off Fast Startup and sleep, so the disk would be cleanly unmounted and the PC would stay awake;
- set the hardware clock to UTC (`RealTimeIsUniversal`), so Windows and Linux agree on the time;
- shrank `C:` to free roughly 800 GB, working around the files Windows can't move while it's running,
  and made a restore point first.

```sh
jkvm key win+x
jkvm snapshot -o s.png            # check the menu opened, then pick Terminal (Admin)
jkvm type --layout en-UK 'Get-Partition -DiskNumber 0 | Format-Table'
jkvm key enter
jkvm snapshot -o s.png            # read the result
```

### 2. BIOS settings and a memory test

The agent rebooted, sent `delete` repeatedly through POST to get into the BIOS, and turned on the RAM's XMP
profile. The BIOS ignored the absolute mouse, so it navigated with arrow keys only, taking a snapshot
after each move. Before saving, it read the BIOS's list of pending changes and checked that the only one
was the one the owner had approved.

To test the new memory settings, it uploaded a MemTest86 image as a virtual disk, used F11 to boot from
it, and ran a 30-minute pass while checking snapshots every few minutes.

```sh
jkvm media mount-file --mode Disk ./memtest86-usb.img    # 1 GiB, about 1.5 minutes
jkvm key f11                                            # sent several times during POST
jkvm snapshot -o s.png
```

### 3. Install Debian from a virtual CD

It mounted the Debian netinst ISO and booted the installer with Secure Boot left on. It used the text-mode
expert installer because a fixed number of key presses reliably moves between its options. A small helper
script made each step one call: send keys, wait, snapshot, crop.

```sh
# kv NAME WAIT KEYS...: press keys, wait, save a snapshot to look at
kv() { name=$1 wait=$2; shift 2; [ $# -gt 0 ] && jkvm key "$@"; sleep "$wait"; jkvm snapshot -o "$name.png"; }

kv lang 1 enter
kv kbd  1 down down enter
```

Partitioning was the dangerous part, so the agent went step by step: it moved the cursor, took a
snapshot to confirm the highlight was on the free space and not on `C:`, and only then pressed Enter.
It stopped for the owner's approval before the first partition-table write. Before carrying on, it
checked from the installer shell (`ctrl+alt+f2`) that the EFI partition and the Windows boot files hadn't
been touched.

It didn't type the root password in a way that could leave it in the terminal or a log. It read the
password from a file, typed it into the masked field, and checked only that the number of asterisks
matched the length of the file.

### 4. Convert to Proxmox over SSH

Once Debian booted and SSH worked, the rest of the job no longer needed the KVM: it added the Proxmox repo
and kernel, set up networking with an automatic rollback in case SSH dropped, created the storage pool, and
joined the cluster. The KVM was still useful for watching reboots. The new kernel left the screen blank
for a while, and a snapshot showed whether that was a hang or the display driver taking over.

### 5. Fix the boot order and Secure Boot keys

The motherboard reset the EFI boot order on every POST and ignored `efibootmgr`, so the agent went back
into the BIOS with the keyboard and set the boot order there. Later, the NVIDIA driver needed a
Machine Owner Key enrolled for Secure Boot. The MOK manager screen appears for only about 10 seconds after
POST, so the agent polled snapshots until the blue screen appeared and pressed a key in time.

Finally it rebooted twice to check both paths: Proxmox by default, and Windows from the GRUB menu, both
with Secure Boot on.

## Lessons

- **Set the right keyboard layout.** With the wrong one, `type` sends the wrong symbols (`"`, `@`, `\`,
  `|`). Use `--layout` or `JKVM_LAYOUT` to match the target, not the device's stored setting.
- **Escapes are off by default.** Without `-e`, `type 'root\n'` types a backslash and an `n`. Send Enter
  with `jkvm key enter`, and never type a secret right after a command that relies on an escape.
- **Use the keyboard in firmware.** BIOS setup screens and boot loaders often ignore the absolute mouse.
- **Snapshot after every step.** It costs about a second and catches cursor-is-one-row-off mistakes before
  they write to a disk.
- **Pause before steps that can't be undone.** The agent asked for approval before partition writes, BIOS
  saves and kernel removal, and showed the snapshot it was acting on.

# JetKVM local protocol (firmware 0.5.9)

What `jkvm` speaks, worked out from the device's own UI bundle (served at
`http://<device>/static/assets/immutable/index-*.js`) and the official firmware source
[github.com/jetkvm/kvm](https://github.com/jetkvm/kvm) at tag `release/0.5.9` (commit
`36c008f`, 2026-09-08). The device under test reports `appVersion 0.5.9`,
`systemVersion 0.2.8`. File references are to that tag.

## HTTP endpoints (`web.go`)

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/device/status` | none | `{"isSetup":true}` |
| POST | `/device/setup` | none | first-run setup |
| POST | `/auth/login-local` | none | `{"password":…}` sets cookie `authToken`; 400 in `noPassword` mode |
| GET | `/device` | yes | `{"authMode":"noPassword","deviceId":"…","loopbackOnly":false}` |
| GET | `/cloud/state` | yes | `{"connected":false,"url":"https://api.jetkvm.com","appUrl":"https://app.jetkvm.com"}` |
| GET | `/webrtc/signaling/client` | yes | WebSocket signaling (below) |
| POST | `/storage/upload?uploadId=…` | yes | raw body upload for virtual media |
| POST | `/auth/logout`, `/auth/password-local` (POST/PUT), DELETE `/auth/local-password` | yes | password management |
| POST | `/cloud/register`, `/device/send-wol/:mac` | yes | |
| GET | `/diagnostics`, `/metrics`, `/developer/pprof/*` | yes / dev mode | |

"Auth yes" means the `authToken` cookie, except in `authMode: noPassword`, where everything is open.

`POST /webrtc/session` (the legacy one-shot HTTP signaling) **no longer exists in 0.5.9**: it
returns 404. It was removed in #1569 ("drop legacy HTTP session signaling"), first shipped in
0.5.9. Any client still using it, such as an old cached UI or older third-party clients, can't connect.

## Signaling WebSocket (`web.go` handleLocalWebRTCSignal, `cloud.go` handleSessionRequest)

All messages are text frames carrying JSON `{"type": …, "data": …}`, except the heartbeat.

1. Device → client on open: `{"type":"device-metadata","data":{"deviceVersion":"0.5.9"}}`.
   The UI waits for this before creating its RTCPeerConnection.
2. Client → device: `{"type":"offer","data":{"sd":"<base64(JSON RTCSessionDescription)>"}}`.
   The JSON is `{"type":"offer","sdp":"v=0…"}`. Optional fields: `ip`, `iceServers`,
   `OidcGoogle` (cloud only).
3. Device → client: `{"type":"answer","data":"<base64(JSON RTCSessionDescription)>"}`.
   The answer SDP carries **no candidates**.
4. Both ways: `{"type":"new-ice-candidate","data":{"candidate":"candidate:…","sdpMid":"0","sdpMLineIndex":0}}`.
   The device trickles its host candidate (e.g. `candidate:… 192.0.2.10 <port> typ host`) right after the answer.
   It sends no end-of-candidates marker.
5. On failure the device sends `{"error": …}`.
6. Heartbeat: the client may send the text `ping` and gets `pong`. The device also sends
   WebSocket ping frames.

ICE configuration: on the LAN the UI passes **no ICE servers** (`iceServers` comes only from
the cloud's `/webrtc/ice_config` when used via app.jetkvm.com). The device's pion stack uses mDNS
`QueryOnly` when `mdns_mode` is not disabled, so it can resolve a browser's `*.local` host
candidates. Connectivity then rests on the device's single host candidate.

A new offer **replaces** the current session. The device sends `otherSessionConnected` to the old
one, clears all keys and closes it 1 s later. When a session closes (ICE closed), the device
releases all keys and cancels any running keyboard macro. Virtual media is unmounted only for
mounts that set `shouldUmountVirtualMedia`, which none of the RPC mounts do. HTTP and storage mounts persist.

## Peer connection

The client is the offerer. The UI adds:

- transceivers `video` recvonly and `audio` recvonly
- data channels: `rpc` (reliable), `hidrpc` (reliable, binary),
  `hidrpc-unreliable-ordered` `{ordered:true,maxRetransmits:0}`,
  `hidrpc-unreliable-nonordered` `{ordered:false,maxRetransmits:0}`, `terminal`, plus later
  `serial`, `cdcacm` and per-upload `upload_<uuid>` channels.

The device handles channels by label (`webrtc.go` OnDataChannel). `jkvm` opens only `rpc` and
`hidrpc` (plus a recvonly video transceiver when it needs the screen).

Video codec: the device picks H.265 if the offer mentions H265 and the preference is `auto`/`h265`,
else H.264 (`resolveCodec`). It sends an IDR only on RTCP feedback (PLI/FIR); a client that never
sends PLI never gets a decodable frame. Video runs only while at least one session is ICE-connected
(`onFirstSessionConnected` / `onLastSessionDisconnected`).

## JSON-RPC on the `rpc` channel (`jsonrpc.go`)

JSON-RPC 2.0. Request `{"jsonrpc":"2.0","method":…,"params":{…},"id":n}` with named params.
Response `{"jsonrpc":"2.0","result":…,"id":n}` or `{"error":{"code":-32601,"message":"Method not found"}}`.
Notifications have no `id`: `{"jsonrpc":"2.0","method":"videoInputState","params":{…}}`.

When `rpc` opens, the device pushes `otaState`, `videoInputState` and `usbState`.
Other notifications: `otherSessionConnected`, `keyboardLedState`, `keysDownState`,
`keyboardMacroState` (the last three only when hidrpc is not in use), `failsafeMode`, `willReboot`.

### Methods (params in parentheses)

Read-only, safe:
`ping`, `getDeviceID`, `getLocalVersion`, `getCloudState`, `getNetworkState`, `getNetworkSettings`,
`getKeyboardLedState`, `getKeyDownState`, `getVideoState`, `getUSBState`, `getJigglerState`,
`getJigglerConfig`, `getTimezones`, `getStreamQualityFactor`, `getSupportedVideoCodecs`,
`getVideoCodecPreference`, `getAutoUpdateState`, `getEDID`, `getEDIDPresets`,
`getHostDisplayIdleMode`, `getVideoLogStatus`, `getVideoSleepMode`, `getDevChannelState`,
`getUpdateStatus`, `getUpdateStatusChannel`, `getDevModeState`, `getSSHKeyState`, `getTLSState`,
`getMassStorageMode`, `isUpdatePending`, `getUsbEmulationState`, `getUsbConfig`,
`checkMountUrl(url)`, `getVirtualMediaState`, `getStorageSpace`, `listStorageFiles`,
`getWakeOnLanDevices`, `getDisplayRotation`, `getBacklightSettings`, `getAudioConfig`,
`getDCPowerState`, `getActiveExtension`, `getATXState`, `getSerialSettings`,
`getSerialCommandHistory`, `getUsbDevices`, `getKeyboardLayout`, `getKeyboardMacros`,
`getLocalLoopbackOnly`, `getDefaultLogLevel`, `getPublicIPAddresses(refresh)`,
`checkPublicIPAddresses`, `getTailscaleStatus`, `getTailscaleControlURL`, `getMqttSettings`,
`getMqttStatus`.

Input (act on the target PC):
`keyboardReport(modifier, keys[])`, `keypressReport(key, press)`, `absMouseReport(x, y, buttons)`,
`relMouseReport(dx, dy, buttons)`, `wheelReport(wheelY, wheelX)`, `wakeHost`,
`sendWOLMagicPacket(macAddress, [broadcastIP])`, `setATXPowerAction(action)`
(`power-short`/`power-long`/`reset`), `setDCPowerState(enabled)`, `sendCustomCommand(command)`.

Virtual media:
`mountWithHTTP(url, mode)`, `mountWithStorage(filename, mode)`, `rpcMountBuiltInImage(filename)`,
`unmountImage`, `startStorageFileUpload(filename, size)`, `deleteStorageFile(filename)`,
`setMassStorageMode(mode)`. `mode` is `"CDROM"` or `"Disk"`.

Device configuration (changes the JetKVM):
`reboot(force)`, `deregisterDevice`, `setNetworkSettings(settings)`, `renewDHCPLease`,
`setJigglerState`, `setJigglerConfig`, `setStreamQualityFactor`, `setVideoCodecPreference`,
`setAutoUpdateState`, `setEDID`, `setHostDisplayIdleMode`, `setVideoSleepMode`,
`setDevChannelState`, `checkUpdateComponents`, `tryUpdate`, `tryUpdateComponents`,
`setDevModeState`, `setSSHKeyState`, `setTLSState`, `setUsbEmulationState`, `setUsbConfig`,
`setWakeOnLanDevices`, `factoryReset`, `setDisplayRotation`, `setBacklightSettings`,
`setAudioConfig`, `setDCRestoreState`, `setActiveExtension`, `setSerialSettings`,
`setSerialCommandHistory`, `deleteSerialCommandHistory`, `setTerminalPaused`, `setUsbDevices`,
`setUsbDeviceState`, `setCloudUrl`, `setKeyboardLayout`, `setKeyboardMacros`,
`setLocalLoopbackOnly`, `setDefaultLogLevel`, `emitTestLog`, `setTailscaleControlURL`,
`setMqttSettings`, `testMqttConnection`.

Sample results from the device under test:

```json
getVideoState    {"ready":true,"error":"","width":1920,"height":1080,"fps":60,"streaming":0}
                 {"ready":false,"error":"no_signal","width":0,"height":0,"fps":0,"streaming":0}
getUSBState      "configured" | "not attached" | ...
getVirtualMediaState  null | {"source":"HTTP"|"Storage","mode":"CDROM"|"Disk","filename":…,"url":…,"size":n}
getStorageSpace  {"bytesUsed":86315008,"bytesFree":14075842560}
listStorageFiles {"files":[{"filename":…,"size":n,"createdAt":…}]}
startStorageFileUpload {"alreadyUploadedBytes":0,"dataChannel":"upload_<uuid>"}
getKeyboardLayout "en-US"
getEDID          "00ffffffffffff00…"   (lower-case hex)
getEDIDPresets   [{"name":"JetKVM Default","edid":"00FFFFFFFFFFFF00…"}, {"name":"Acer B246WL, 1920x1200",…}, …]
setEDID          ({"edid":"<hex>"}) → null; the target re-reads the EDID and video drops for a few seconds
```

Preset EDIDs are upper-case hex, `getEDID` lower-case, so compare them ignoring case.

### Virtual media upload (`usb_mass_storage.go`)

1. `startStorageFileUpload(filename, size)` returns `{alreadyUploadedBytes, dataChannel}`.
   The device appends to `<name>.incomplete`, so a retry resumes from `alreadyUploadedBytes`.
   It fails if `<name>` already exists.
2. Send the remaining bytes either as `POST /storage/upload?uploadId=<dataChannel>` (raw body) or
   over a data channel labelled `<dataChannel>` (the device replies with
   `{"Size":…,"AlreadyUploadedBytes":…}` progress text). The transport must start within 60 s.
3. When the byte count reaches `size`, the device renames the file into place.
   `listStorageFiles` confirms it.
4. `mountWithStorage(filename, "CDROM")`.

`mountWithHTTP(url, mode)` serves the image over NBD from an HTTP(S) server that supports range
requests. `checkMountUrl(url)` validates the URL first. Only one image can be mounted at a time
("another virtual media is already mounted"). Storage is about 14 GB.

## hidrpc binary channel (`internal/hidrpc`, `hidrpc.go`, UI `hooks/hidRpc.ts`)

Each message is one type byte plus a payload. Multi-byte integers are big-endian.

| Type | Name | Direction | Payload |
|---|---|---|---|
| 0x01 | Handshake | both | `[version=0x01]`. The client sends it and retries until the device echoes it |
| 0x02 | KeyboardReport | → device | `[modifier, key1..key6]` (fewer keys are zero-padded). A full state report with no auto-release |
| 0x03 | PointerReport | → device | `[x int32, y int32, buttons u8]`, absolute 0..32767 |
| 0x04 | WheelReport | → device | defined but **not handled in 0.5.9**. Use JSON-RPC `wheelReport` |
| 0x05 | KeypressReport | → device | `[key, press(0/1)]`. A press auto-releases after 100 ms unless 0x09 keep-alives arrive every ~50 ms |
| 0x06 | MouseReport | → device | `[dx int8, dy int8, buttons]` relative |
| 0x07 | KeyboardMacroReport | → device | `[isPaste u8, stepCount u32, steps…]`. Each step is `[modifier, keys[6], delayMs u16]` (9 bytes). The device sets the report, then sleeps for the delay |
| 0x08 | CancelKeyboardMacro | → device | none |
| 0x09 | KeypressKeepAlive | → device | none |
| 0x32 | KeyboardLedState | ← device | `[bits]` |
| 0x33 | KeysDownState | ← device | `[modifier, keys…]` |
| 0x34 | KeyboardMacroState | ← device | `[running, isPaste]`. 1 when a macro starts, 0 when it ends |

The UI pastes text as macro steps per character: `{mod, [key], 20 ms}` and then `{0, [], delay}`
(default delay 20 ms). It sends at most 128 steps per report (64 KiB channel message limit) and waits
for `0x34 [0,…]` before sending the next chunk. Mouse buttons: left 1, right 2, middle 4.

### Modifier bits

| Bit | Key |
|---|---|
| 0x01 | Left Ctrl |
| 0x02 | Left Shift |
| 0x04 | Left Alt |
| 0x08 | Left Meta / Win |
| 0x10 | Right Ctrl |
| 0x20 | Right Shift |
| 0x40 | Right Alt / AltGr |
| 0x80 | Right Meta |

### Key usage codes (`ui/src/keyboardMappings.ts`, USB HID usage page 7)

```
a..z 0x04..0x1d   1..9 0x1e..0x26   0 0x27   Enter 0x28   Escape 0x29   Backspace 0x2a
Tab 0x2b   Space 0x2c   Minus 0x2d   Equal 0x2e   BracketLeft 0x2f   BracketRight 0x30
Backslash 0x31   HashTilde(non-US #) 0x32   Semicolon 0x33   Quote 0x34   Backquote 0x35
Comma 0x36   Period 0x37   Slash 0x38   CapsLock 0x39   F1..F12 0x3a..0x45
PrintScreen 0x46   ScrollLock 0x47   Pause 0x48   Insert 0x49   Home 0x4a   PageUp 0x4b
Delete 0x4c   End 0x4d   PageDown 0x4e   Right 0x4f   Left 0x50   Down 0x51   Up 0x52
NumLock 0x53   KP/ 0x54  KP* 0x55  KP- 0x56  KP+ 0x57  KPEnter 0x58  KP1..9 0x59..0x61
KP0 0x62  KP. 0x63   IntlBackslash(non-US \|) 0x64   ContextMenu 0x65   Power 0x66
F13..F24 0x68..0x73   modifiers as keys: 0xe0..0xe7 (LCtrl LShift LAlt LMeta RCtrl RShift RAlt RMeta)
```

### Layouts (`ui/src/keyboardLayouts/*.ts`)

The firmware ships 19 layouts (cs_CZ, da_DK, de_CH, de_DE, en_UK, en_US, es_ES, fr_BE, fr_CH,
fr_FR, hu_HU, it_IT, ja_JP, nb_NO, pl_PL, pt_PT, ru_RU, sl_SI, sv_SE). The device stores one
(`getKeyboardLayout`, `en-US` by default). It only affects how the UI maps characters when pasting: the
device itself sends raw usage codes, and the **target OS** layout decides which character appears.
en-UK differs from en-US in these keys:

| char | en-US | en-UK |
|---|---|---|
| `"` | Shift+Quote | Shift+2 |
| `@` | Shift+2 | Shift+Quote |
| `#` | Shift+3 | Backslash (0x31, the UK #~ key) |
| `~` | Shift+Backquote | Shift+Backslash |
| `\` | Backslash | IntlBackslash (0x64) |
| `\|` | Shift+Backslash | Shift+IntlBackslash |
| `£` | – | Shift+3 |
| `€` | – | AltGr+4 |
| `¬` | – | Shift+Backquote |

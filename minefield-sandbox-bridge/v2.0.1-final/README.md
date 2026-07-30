# Minefield Sandbox Bridge v2.0.1 — Live Windows Closure

Status: **LIVE_WINDOWS_CLOSURE_VERIFIED** on DESKTOP-01B08BS (2026-07-30).

This source and binary remove the assistant-sandbox → authenticated Windows Minefield execution boundary. The initial bootstrap used this dedicated GitHub branch because Remote Desktop Commander could not dereference sandbox:/mnt/data paths. The installed steady-state path no longer depends on that bootstrap: Minefield Control 2.0.1 acquires exact-origin ChatGPT ZIP links and the loopback broker accepts authenticated, resumable rtifact.push.* operations.

## Live installed endpoints

- Control broker: 127.0.0.1:9789
- Global chat locator peer: 127.0.0.1:9791
- Stable extension ID: inhjgnkfjaehkgjonheafkcpbfejpjjh
- Active extension version: 2.0.1
- Broker version: 2.0.1

## Permanent artifact protocol

push.begin → push.chunk → push.status → push.commit or push.abort.

Every operation is token-authenticated, exact-origin bound, idempotent, SHA-256 checked, same-volume committed, and evidence bearing. Numbered duplicate filenames, conflicting chunks, wrong origins, unsafe ZIPs, and existing extraction targets fail visibly without merge/overwrite.

## Windows-only defects repaired during live execution

1. Explicitly close the ZIP reader before atomic commit/deletion; retain deferred close on every early return.
2. Convert optional boundary values with stringAny; 
il becomes an empty string instead of the invalid literal <nil>.
3. Retire only the exact superseded MinefieldTotalControl broker that occupied locator port 9791.
4. Reload only the stable-ID Minefield extension in the exact Canary PID/profile through CUA, without restarting the browser.

## Verification

- go test ./... — PASS
- go vet ./... — PASS
- Debian go test -race ./... — PASS
- two byte-identical Windows GUI builds — PASS
- exact-origin chunk upload/commit/extract/delete/return — PASS
- duplicate chunk idempotency and conflict rejection — PASS
- origin mismatch and (1).zip rejection — PASS
- existing target refusal with marker preservation — PASS
- exact-origin PowerShell through broker, exit code 0 — PASS
- active extension version 2.0.1 + service worker observed through CUA — PASS

## Hashes

- Windows executable SHA-256: $(@{schema=doubletab/minefield-sandbox-bridge-live-closure/1; at=2026-07-30T14:24:27.6811350Z; status=LIVE_WINDOWS_CLOSURE_VERIFIED; objective=Make sandbox-originated work executable against the authenticated Windows Minefield installation without human file shuttling; limitations=System.Object[]; transport=; broker=; extension=; build=; acceptance=; protectedState=; remainingBoundary=NONE_FOR_AUTHORIZED_LOCAL_EXECUTION; nextAutomaticAcceptance=The next newly generated .zip link in an exact-origin Minefield ChatGPT tab is acquired without a human click and returns its receipt through the installed channel.}.build.windowsExecutableSha256)
- Source-tree SHA-256: $(@{schema=doubletab/minefield-sandbox-bridge-live-closure/1; at=2026-07-30T14:24:27.6811350Z; status=LIVE_WINDOWS_CLOSURE_VERIFIED; objective=Make sandbox-originated work executable against the authenticated Windows Minefield installation without human file shuttling; limitations=System.Object[]; transport=; broker=; extension=; build=; acceptance=; protectedState=; remainingBoundary=NONE_FOR_AUTHORIZED_LOCAL_EXECUTION; nextAutomaticAcceptance=The next newly generated .zip link in an exact-origin Minefield ChatGPT tab is acquired without a human click and returns its receipt through the installed channel.}.build.sourceTreeSha256)
- Live-closure JSON SHA-256: 340b14d596bda4f1b4c8dfe43f523f607d83939ccffb3b508d00d25e2716c63d

The complete machine-readable proof is evidence/SANDBOX-BRIDGE-LIVE-CLOSURE.json.
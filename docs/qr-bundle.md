# QR-encoded bundle format

Track L. Defines the wire format used to deliver an offline-CA-signed CBOR
enrollment bundle to a mobile or otherwise air-gapped goat-client over a QR
code (or a paste-in-channel string).

## Goals

- Single-shot delivery: one QR code, one scan, one `Decode()` call. No
  splitting, no chunk reassembly, no chunk-ordering metadata.
- Survive being printed and re-photographed: pick QR parameters that leave
  enough margin for noisy scans while still fitting on a phone screen at
  arms' length.
- Round-trip identity: the bytes that come out of `Decode()` must equal the
  bytes the operator handed to `Encode()`. Verification (ECDSA P-256 against
  the pinned offline-CA root, post-Block-79 cutover) is done downstream by
  `internal/bundle/`, not here.

## Sizing budget

The offline-enrollment CBOR bundle (`docs/design/offline-enrollment.md` in
`dlf-dds/DesertBreadBird`) is approximately 1.5 kB for a single peer:
issued-to, site, NotBefore/NotAfter, peer Curve25519 pubkey, endpoint list,
ECDSA P-256 ASN.1-DER signature, and the CA cert chain.

QR alphanumeric mode encodes 11 bits per pair of characters (vs. 8 bits per
byte in byte mode), and the 45-character alphanumeric alphabet is exactly
the base45 (RFC 9285) alphabet. So a base45-encoded payload travels at
maximum density inside a QR alphanumeric-mode region. base45 expands binary
by 50% (every 2 bytes → 3 chars; an odd trailing byte → 2 chars), so we
need an alphanumeric capacity of `ceil(N_bytes/2)*3` characters to carry
`N_bytes` of binary.

QR-version capacities at ECC level **L** (ISO/IEC 18004 Table 7,
alphanumeric mode):

| QR version | Alphanumeric chars | Max binary via base45 |
|------------|--------------------|-----------------------|
| 25         | 2,520              | ~1,680 B              |
| 27         | 2,840              | ~1,893 B              |
| 30         | 3,351              | ~2,234 B              |
| 40         | 4,296              | ~2,864 B              |

A 1.5 kB bundle fits comfortably in QR-25/L (≈1,680 B of headroom). A
bundle that grows toward 2 kB still fits in QR-30/L. Beyond ≈2,860 bytes,
even QR-40/L can no longer hold the base45 payload; the operator must
either trim the bundle schema (drop redundant cert intermediates, prefer
shorter endpoint hostnames) or move to a non-QR delivery channel. v1 does
not implement multi-frame QR splitting — `Encode()` returns an error if
the input would exceed the QR-40/L alphanumeric ceiling.

We pick **error-correction level L** (~7% recoverable) deliberately:
bundles are short-lived (90-day NotAfter), the operator scans in person on a
clean screen or freshly-printed page, and the delivery channel is
authenticated by the ECDSA P-256 signature inside the CBOR — a flipped bit
either decodes to a valid CBOR with a bad signature (rejected downstream)
or fails CBOR parse (rejected downstream). We do not need the QR code
itself to be self-correcting beyond what L provides, and L gives us the
most payload headroom.

## Encoding pipeline

```
bundle.cbor (binary)
   │
   ▼  base45 encode (RFC 9285)
ASCII string over the 45-char QR alphanumeric set
   │
   ▼  QR encode (alphanumeric mode, ECC L, version auto-selected)
QR code (PNG for screen/print, or the raw base45 string for paste delivery)
```

`Encode(bundleBytes)` returns the base45 string. Rendering that string into
a PNG QR is done by the operator tool (`cmd/goat-bundle-qr`) using
`github.com/skip2/go-qrcode`; the package itself does not depend on a QR
rendering library so it stays usable from mobile clients via gomobile (Track
C iOS, Track D Android) where the QR scanner UI is platform-native and
only `Decode()` is needed.

## Why base45, not base64

RFC 9285 base45 was designed for exactly this: dense binary payloads in QR
alphanumeric mode. base64 contains lowercase letters and `/`, which are not
in the QR alphanumeric set, so a base64 payload would force QR byte mode and
inflate the module count. base32 is in the alphanumeric set but expands by
60% vs. base45's 67% — close — but base45 is the IETF-blessed choice for QR
and aligns us with EU Digital COVID Certificate / DGC tooling, which is the
largest deployed user of CBOR-over-QR.

## Why no chunking

The CBOR bundle today fits in a QR-25 with room to spare. If the bundle
schema grows — e.g., adding a multi-peer config or a longer cert chain —
the right answer is to revisit the schema, not to add a chunked-QR
reassembly layer to mobile clients. Multi-frame QR (animated or N-of-M
fountain codes) trades operator UX for schema laziness, and mobile QR-scan
UIs would have to grow a "keep scanning" state machine. Out of scope for v1.

`Encode()` returns an error if the input would force a QR version above 30
or exceed the ECC-L byte budget.

## Mobile consumer contract

Track C (iOS NEPacketTunnelProvider + Swift) and Track D (Android VpnService
+ Kotlin) ship a platform-native QR scanner UI (AVCaptureSession on iOS,
CameraX + ML Kit on Android). When the scanner reports a decoded payload as
a UTF-8 string, the shell calls into the gomobile-exported
`qrbundle.Decode(payload string) ([]byte, error)`. The returned bytes are
the original CBOR, ready to hand to `internal/bundle/`'s parse + verify.

Track B's Fyne bundle-import dialog does the same thing for the desktop
"paste a base45 string" code path: the user pastes the string the operator
sent in a chat channel, and the dialog calls `Decode()` before invoking the
existing `Preview()` flow.

## Operator workflow

1. Operator runs `goat-bundle-create` (Track A) on the offline-CA
   workstation and gets `bundle.cbor`.
2. Operator runs `goat-bundle-qr -in bundle.cbor -out bundle.png` on the
   same workstation. The tool prints the base45 string to stdout (for
   paste-in-channel delivery) and writes a PNG of the QR (for
   show-on-screen / print delivery).
3. Operator delivers either artifact to the end user out-of-band.
4. End user scans the QR with the goat-client mobile app, or pastes the
   string into the desktop bundle-import dialog. The app calls `Decode()`,
   then hands the resulting CBOR to the daemon's `importBundle` IPC.

The PNG is suitable for embedding in a printed enrollment letter or showing
on a kiosk screen. Authentication is provided by the ECDSA P-256 signature
inside the CBOR — the QR is just transport.

# From-scratch operator runbook — Apple + Google developer accounts

Source of truth for the one-time, non-scriptable steps an operator
must perform to bootstrap mobile-app distribution for goat-client (or
any successor mobile app). Captured retroactively from the 2026-05-14
TestFlight + Play Internal bring-up so a future operator can retrace
without rediscovering each gotcha.

For the *recurring* automation that runs after this bootstrap, see
[`README.md`](README.md) in this directory.

---

## What this covers

Every step where Apple's or Google's UI is the only way through —
identity verification, payment, account creation, first-time
capability toggles. After completing these once, everything else
moves to the API-driven scripts in this directory.

## Total elapsed time

- **Best case (everything goes right):** ~3 hours active + 2-3 days
  passive waiting for identity verification.
- **Realistic (you hit at least one gotcha):** ~6-8 hours active over
  2-7 elapsed days.

The longest items are Apple's identity-verification window (1-3
business days for Individual, 1-2 weeks for Org needing a fresh
D-U-N-S) and the Play Console device-verification gate.

---

## Cost summary

| Item | Cost | Cadence |
|---|---|---|
| Apple Developer Program (Individual) | $99 USD | Annual auto-renew |
| Apple Developer Program (Organization) | $99 USD + D-U-N-S (free) | Annual |
| Google Play Developer Account | $25 USD | One-time |
| Android upload-key keystore | Free (`keytool`) | One-time (non-rotatable) |
| App Store Connect API key | Free | Free; generate at will, max 50 per team |
| Google Play Developer service-account JSON | Free | Free; rotate as needed |

Recurring infra cost (engineering builds): **$99/yr**.

---

## Apple — from scratch

### Phase A.1 — Enroll in Apple Developer Program

1. Visit [developer.apple.com/programs/enroll](https://developer.apple.com/programs/enroll).
2. Sign in with the Apple ID that should own the developer team. For
   `dlf-dds` use a real human's Apple ID (we used `dene.farrell@gmail.com`).
3. Choose entity:
   - **Individual** — fastest (1-3 business days verification). Email
     + 2FA-to-iPhone is the identity proof. Use this for
     small teams unless you need an Organization-tier App Store
     Connect listing.
   - **Organization** — requires D-U-N-S number (free lookup at
     [dnb.com/duns-number/lookup](https://www.dnb.com/duns-number/lookup.html);
     1-2 weeks if you need a new D-U-N-S issued). Use this when the
     legal-entity name matters on the App Store listing.
4. Pay $99 (auto-renews annually). Receipt comes from Apple Distribution International.
5. Wait for the welcome email: "Welcome to the Apple Developer Program."

**Common pitfall:** the post-purchase Apple Developer Portal page says
"Purchase your membership — Your purchase may take up to 48 hours to
process." This is misleading — the purchase IS recorded; that banner
means identity-verification processing. Don't pay twice.

**What to capture for the team:** the **Team ID** (10-char alphanumeric
shown on developer.apple.com/account top right). Save to repo
`.envrc.local` as `APPLE_DEVELOPMENT_TEAM`.

### Phase A.2 — Register App IDs + capabilities

For each app (we have two for goat-client: the main app + the
PacketTunnel extension), do the following in
[developer.apple.com/account/resources/identifiers/list](https://developer.apple.com/account/resources/identifiers/list).

Future: `apple/setup_app_ids.py` automates this end-to-end via
ASC API. For first-time bootstrap when the script doesn't exist yet:

1. **+** button → App IDs → App → Continue.
2. Description: human-readable name (e.g. `goat-client`).
3. Bundle ID: **Explicit** → `io.dlf-dds.goat-client` (use dashes;
   `_` not allowed for iOS bundle IDs).
4. Scroll to Capabilities → check **Network Extensions** + **App Groups**.
5. Continue → Register.

Repeat for `io.dlf-dds.goat-client.PacketTunnel`.

Then create the App Group:

1. Identifiers → dropdown to **App Groups** → **+**.
2. Description: `goat-client app group`. Identifier:
   `group.io.dlf-dds.goat-client`.
3. Continue → Register.

Then link the App Group to both App IDs:

1. Identifiers → App IDs → click `io.dlf-dds.goat-client`.
2. Scroll to **App Groups** → **Edit** → check the new group →
   **Save**.
3. Repeat for `io.dlf-dds.goat-client.PacketTunnel`.

**Common pitfall — gotcha that cost us hours:** clicking Save while
no changes were pending leaves the capability un-saved on Apple's
backend, even though the UI shows the checkbox checked. **Always
toggle the checkbox OFF, then ON, then click Save** to force a real
change → real persistence. The Save button must un-grey before
clicking. Confirms with a "Modify App Capabilities?" prompt.

### Phase A.3 — App Store Connect API key (for automation)

[appstoreconnect.apple.com/access/integrations/api](https://appstoreconnect.apple.com/access/integrations/api).

1. **Team Keys** tab → **Generate API Key**.
2. Name: `<repo>-uploads-admin` (we used `goat-client-uploads-admin`).
3. Access: **Admin**.
   - **Critical**: Admin role is required for `xcodebuild`
     cloud-signing to create Distribution certs + provisioning
     profiles. App Manager is enough for plain upload but NOT for
     cert creation. We hit this gotcha — generated key with App
     Manager first, had to regenerate with Admin.
4. **Generate** → **Download** the `.p8` file (one-time only — if
   lost, regenerate).
5. Save the `.p8` to `~/.appstoreconnect/AuthKey_<KEY_ID>.p8`
   (mkdir the directory first).
6. **Note** the Key ID + the Issuer ID (Issuer ID is shown at the
   top of the API access page header; same for all keys on the team).
7. Save to `.envrc.local`:
   ```
   export ASC_API_KEY_ID="..."          # 10-char Key ID
   export ASC_API_ISSUER_ID="..."       # UUID
   export ASC_API_KEY_PATH="$HOME/.appstoreconnect/AuthKey_${ASC_API_KEY_ID}.p8"
   ```

### Phase A.4 — Register the app in App Store Connect

[appstoreconnect.apple.com/apps](https://appstoreconnect.apple.com/apps)
→ **+** → **New App**.

Fill:
- Platforms: iOS.
- Name: `goat-client` (this is the App Store listing name; visible to
  testers).
- Primary language: English (U.S.).
- Bundle ID: select `io.dlf-dds.goat-client` from the dropdown
  (Phase A.2 must have completed).
- SKU: any unique string (we used `goat-client`).
- User Access: Full.

Click Create.

### Phase A.5 — Internal tester enrolment (ONE permanent UI step)

This is the only step in the Apple flow with no API equivalent.
After the first TestFlight build is uploaded + processed, navigate
in App Store Connect:

1. Apps → **goat-client** → **TestFlight** tab.
2. Left sidebar → **Internal Group** → click your group
   (`Internal pilot`, created by `apple/manage_testers.py setup`).
3. **Testers** tab → **+** → select team members from the dropdown
   (only team Users appear — these are people with App Store
   Connect access for this team).
4. Save.

After this, all subsequent builds uploaded for the same app + same
beta group auto-distribute to those testers. So this is a
**one-time setup per app per tester**.

**Why API doesn't cover this:** Apple's ASC API exposes
`/v1/betaTesters` for *external* testers only. For team-User
internal testers, both
`/v1/users/{id}/relationships/betaGroups` and
`/v1/betaGroups/{id}/relationships/users` return `404 The
relationship does not exist`. The fact that you have a team User
with admin role does not automatically grant TestFlight visibility
of internal builds. Confirmed against the live API 2026-05-14.

---

## Google — from scratch

### Phase G.1 — Google Play Developer account

1. [play.google.com/console/signup](https://play.google.com/console/signup)
   → sign in with the Google account that should own the listings.
2. Account type:
   - **Personal** — $25 one-time, ~1-2 day verification, no D-U-N-S.
     Use this for small teams / individuals.
   - **Organization** — same $25 + business verification (D-U-N-S or
     equivalent). Use when the legal-entity name matters for Play
     Store visibility.
3. Pay $25, complete identity verification.

**What to capture:** the **Account ID** (visible at
[play.google.com/console](https://play.google.com/console) →
profile → developer details). Save to `.envrc.local` as
`GOOGLE_PLAY_ACCOUNT_ID`.

### Phase G.2 — Gate B: Play Console device verification

A relatively recent (2023+) requirement for new Personal accounts:
install the **Play Console** mobile app on a **real Android device**,
sign in with the developer Google account, complete the in-app
verification flow.

**Cannot be done on an Android emulator.** Google's verification
uses Play Integrity attestation which `google_apis_playstore`
emulator images often pass on other surfaces but Play Console
specifically refuses. We tried Pixel_3a_API_33 with Play Store
enabled; the emulator also crashed under macOS 26 windowed-mode in
a separate issue (kernel/HVF compatibility — does NOT affect headless
emulator use for normal Android dev).

If you don't have an Android device:
- Borrow one (any Android 7+ device works for the verification).
- Buy a cheap Pixel ($80-150 on Swappa for a used Pixel 4a / 5a / 6a).

After the in-app verification succeeds, the Play Console web UI
unblocks. **One-time gate per developer account.**

### Phase G.3 — Create the app listing

1. [play.google.com/console](https://play.google.com/console) →
   **Create app**.
2. App name: `goat-client`.
3. Default language: English (U.S.).
4. App or game: App.
5. Free or paid: Free.
6. Check both declarations (developer-program-policies +
   US-export-laws).
7. Create.

You're now at the app dashboard. **Don't worry about the production
listing fields** (icon, screenshots, store listing, privacy policy
URL, etc.) — Play Console requires those before a public Production
release but does NOT require them for Internal Testing track uploads,
which is all we use for engineering builds.

**Common pitfall:** the AAB upload will reject if `targetSdk` is
below Google's current floor. We hit this when our build targeted
API 34; Google requires API ≥35 as of 2026-Q2. Bump
`mobile/android/Shell/app/build.gradle.kts` if the build complains.

### Phase G.4 — Android upload keystore

One-time, machine-local. Google holds the actual app-signing key
("Play App Signing"); the upload key is what *you* use to authenticate
uploads against Play. **The upload key is NOT rotatable** — pick a
passphrase, store it everywhere durable.

```bash
mkdir -p ~/.android
PASS="$(openssl rand -base64 32)"
JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home"
PATH="$JAVA_HOME/bin:$PATH" keytool -genkey -v \
    -keystore ~/.android/goat-client-upload.keystore \
    -keyalg RSA -keysize 2048 -validity 10000 \
    -alias goat-upload \
    -storepass "$PASS" -keypass "$PASS" \
    -dname "CN=goat-client upload key, O=dlf-dds, C=US"

# Store the password in macOS Keychain (primary):
security add-generic-password \
    -a goat-client -s goat-client-upload-key -w "$PASS" -U

# Also write to .envrc.local (backup):
echo "Add to .envrc.local:"
echo "  export ANDROID_UPLOAD_KEYSTORE=\"\$HOME/.android/goat-client-upload.keystore\""
echo "  export ANDROID_UPLOAD_KEY_ALIAS=\"goat-upload\""
echo "  export ANDROID_UPLOAD_KEY_PASSWORD=\"$PASS\""

# Tertiary backup: paste $PASS into 1Password under a new login item titled
# "goat-client Android upload keystore" with notes referencing the keystore path.
```

The `app/build.gradle.kts` signing config (already wired) reads the
password from `ANDROID_UPLOAD_KEY_PASSWORD` env-var with fallback to
the macOS Keychain entry. Triple-storage is intentional: if any one
mechanism breaks (keychain corrupt, .envrc.local lost, 1Password
locked) the others recover.

### Phase G.5 — Play Developer service account (for automation)

Needed for `google/upload_to_play_internal.py` etc.

1. Play Console → **Setup** → **API access**.
2. Link a Google Cloud project (default offered; accept).
3. Service accounts section → **Create new service account**.
4. Google Cloud Console opens. Fill:
   - Service account name: `goat-client-play-publisher`.
   - Description: `Play Developer API client for goat-client mobile uploads`.
   - **Create and Continue**.
   - Role: **Service Account User**.
   - Continue → Done.
5. Click your new account → **Keys** tab → **Add Key → Create new key
   → JSON** → Create. JSON downloads.
6. Save locally:
   ```bash
   mkdir -p ~/.gcloud
   mv ~/Downloads/goat-client-play-publisher-*.json \
       ~/.gcloud/play-publisher-goat-client.json
   chmod 600 ~/.gcloud/play-publisher-goat-client.json
   ```
7. **Back in Play Console → API access**. Find your service account
   in the list → **Grant access** → assign to the goat-client app
   with: **View app information** + **Manage testing tracks**
   (minimum). For full automation including listing edits, also add
   **Manage store presence**.
8. Add to `.envrc.local`:
   ```
   export GOOGLE_PLAY_SERVICE_ACCOUNT_KEY="$HOME/.gcloud/play-publisher-goat-client.json"
   ```

### Phase G.6 — Internal Testing tester list (per release)

For each tester you want to add to the Internal track:

1. Play Console → **goat-client** → **Testing** → **Internal testing**
   → **Testers** tab.
2. **Manage testers** → **Create email list** (or edit the one you
   made) → add Google account emails one per line → Save.
3. From "How testers join your test" section → copy the opt-in URL.
4. Tester opens that URL **on the same Android device where Play
   Store is signed into the same Google account**.

**Critical**: the tester's email must match the Play Store account on
their phone. Adding `personal@gmail.com` to the tester list while
the phone is signed into `work@gmail.com` → the opt-in URL won't
grant install permission. Verify by opening Play Store on the
phone → profile circle (top right) → confirm the visible email
matches what's on the tester list.

This step has API equivalent (see `google/manage_testers.py`); only
the very first tester is typically added via UI to verify the flow.

---

## Cross-platform summary

After completing all Phase A + Phase G steps (one-time), your
`.envrc.local` carries:

```bash
# Apple
export APPLE_DEVELOPMENT_TEAM="..."
export ASC_API_KEY_ID="..."
export ASC_API_ISSUER_ID="..."
export ASC_API_KEY_PATH="$HOME/.appstoreconnect/AuthKey_${ASC_API_KEY_ID}.p8"

# Google
export GOOGLE_PLAY_ACCOUNT_ID="..."
export ANDROID_PACKAGE_NAME="io.dlf_dds.goat_client"
export GOOGLE_PLAY_SERVICE_ACCOUNT_KEY="$HOME/.gcloud/play-publisher-goat-client.json"
export ANDROID_UPLOAD_KEYSTORE="$HOME/.android/goat-client-upload.keystore"
export ANDROID_UPLOAD_KEY_ALIAS="goat-upload"
export ANDROID_UPLOAD_KEY_PASSWORD="..."
```

And outside the repo, you have:
- `~/.appstoreconnect/AuthKey_<KEY>.p8` — ASC API private key
- `~/.android/goat-client-upload.keystore` — Android upload keystore
- `~/.gcloud/play-publisher-goat-client.json` — Play Developer service account
- 1Password / Keychain entries for the keystore password

Then every build + upload + tester management goes through the
scripts in `mobile/scripts/apple/` and `mobile/scripts/google/`. No
more browser sessions for routine work.

---

## Recovery scenarios

### Lost ASC API key (`.p8` file)

Generate a new key (Phase A.3). Update `.envrc.local`. The old key
can be revoked from the API access page to avoid token-leakage risk.

### Lost Apple Developer Team access

If the team owner's Apple ID is lost (death, departure, account
compromise): contact Apple Developer Support for an ownership
transfer. Requires legal documentation (especially for Org
accounts). Plan ahead — have a backup ADMIN user on the team.

### Lost Android upload keystore

Google has a one-time recovery flow for lost upload keys: Play
Console → App integrity → upload-key reset request. Takes 1-2
business days. After recovery, all developers must use the new
upload key. The *app-signing* key (held by Google) is never lost
unless you opted out of Play App Signing.

### Lost Play Developer service-account JSON

Generate a new JSON key in Google Cloud Console → IAM → service
accounts. Delete the old one. The service account itself doesn't
change — only the key material is rotated.

### Re-running the whole bootstrap

If you're standing up a NEW app from scratch (different `applicationId`,
different team, new Apple ID), redo Phase A.1-A.5 + Phase G.1-G.6.
Most steps are idempotent or skip-able if state already exists:

```bash
# Once future tooling exists:
python mobile/scripts/apple/setup_app_ids.py    # idempotent
python mobile/scripts/google/setup_app_listing.py # idempotent (mostly)
```

Anything that genuinely requires a browser (Apple Dev Program
enrollment, Play account verification, Gate B) is in this runbook;
nothing else should require browser interaction.

---

## When this runbook is wrong

Apple and Google both reorganise their consoles regularly. If
clicks don't match this runbook anymore, the underlying API endpoints
generally stay stable (versioned at `v1`). Prefer fixing the API
scripts to chasing UI changes. Update this runbook by appending a
"Drift YYYY-MM" section rather than editing in place — historical
operators sometimes need the old click path to debug their own
backups.

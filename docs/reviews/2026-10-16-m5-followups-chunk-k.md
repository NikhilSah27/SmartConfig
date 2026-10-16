# Review of chunk K: the M5 follow-ups' secrets

Date: 2026-10-16, after M5 follow-ups 6 and 7 were built (`9ef175e..63e60d2`):
- a secret list in the scope: content stored, its secret values shown hidden (`47d35a5`);
- `--show-secrets` shows them, as root (`1d6671a`).

**How it was reviewed:**
- **The reviewer:** one AI agent, in its own clone of `63e60d2` (pushing disabled), with
  its own `sc` builds and throwaway stores.
- **The limits:** no sudo, nothing written to `/etc` or the real store, no QEMU, and no real
  secret file read: every value in its tests and report is made up. For netplan's forms it
  ran netplan's own generator (1.1.2) on look-alike files with `--root-dir`.
- **Claude's checks:**
  - reran the reviewer's 16 failing tests on `63e60d2`: all 16 fail, and its 2 passing
    tests pass (`~/smartconfig-work/review-m5fu-k/scratch/verify-main-session.log`);
  - checked K1's paths with `sc scope`;
  - every fix has a test that fails without it;
  - mutations of the fixes: 15 for fix 1 and 20 for fix 2, all killed
    (`~/smartconfig-work/fu-scratch/k-mut/`); the review's own, re-anchored on the fixed
    code with about 30 more, 81 of 82 killed, the survivor unreachable
    (`~/smartconfig-work/k-fix/c/scratch/results.txt`); and spot checks of fixes 3 and 4.
- **The fixes:** fixes 1 and 2 were built in this session. At the owner's ask ("make
  several agents to fix all these faster"), fix 3 (K7, K6), K9 with K13, and K12's tests
  were built by three agents, each in its own clone of `c16f948`; Claude applied, checked
  and committed each patch.
- **Report:** `~/smartconfig-work/review-m5fu-k/findings.md`; its tests and scripts in
  `clone/` and `scratch/`.

**Gate: one high-severity finding, K1, fixed.** Three were medium (K2, K3, K4). All 13 are
fixed. Two asked for a decision, and the owner picked the recommended answer for each
(2026-10-16): K1 and K7.

## What held up

- **`--show-secrets`.** As a user, with `SC_HOME` set, `sc cat`, `sc diff` and `sc check`
  refuse it (exit 1) before reading anything. It goes back to false on every run. No other
  `PreRunE` is replaced. `sc edit` and `sc restore` always hide. The store's 0700 and 0600
  match the root check.
- **Shadow.** Empty, `!`, `!!`, `*` and `!*` stay; `NP` and odd hashes are hidden; a line
  with no colon is hidden whole; CRLF and a missing last newline keep their bytes.
- **Keyfiles.** `psk`, `wep-key0..3`, `leap-password`, `password`, `password-raw`,
  `private-key-password` and `phase2-...`, `pin`, `[vpn-secrets]`, WireGuard's keys, gsm,
  pppoe and adsl passwords, localised keys and blanks around `=` are hidden; `-flags` and
  `-type` keys are kept.
- **Netplan.** Block, folded, quoted, flow, tab, comment and continued plain values are
  hidden. Netplan refuses anchors and aliases, so they are no risk.
- **Logs and status.** scd's journal lines carry only a finding's text and intents.
  `sc status` and the console report show only the rule and line. `sc log` shows no
  content. Netplan's quoted YAML line is dropped, and netplan echoed no secret in 13
  malformed forms. The account checkers run against a made-up shadow.
- **The real path.** `--as`, `sc check <id>`, `sc edit` and `sc restore` judge by it.
- **What scd records.** `/etc/shadow-`, `/etc/gshadow-`, `opasswd`, `99-installer.cfg` and
  editor and dpkg copies are excluded. A digest line wins over a secret line.

## Findings

| Area | Finding | Severity | Fixed |
|---|---|---|---|
| the list (K1) | About 40 more files that hold passwords or keys were stored and shown in full: `/etc/fstab`'s CIFS options, sssd, wpa_supplicant, cloud-init, ifupdown, VPN and IPsec, mail relays, LDAP and Kerberos secrets, Let's Encrypt keys, and copies of shadow by any name. Several were in no format the redaction read. | high | Your pick: widen the list and keep the content, so `sc restore` can still put any of them back. More `secret` lines; a new verb, `hide`, for files that are only a secret, shown with every line hidden; shadow's rule for any file named after it and a crypt hash in shadow's place anywhere; `key value` lines; OpenVPN's inline keys; more key names (`c404bd1`). |
| netplan (K2) | Forms netplan accepts still showed a Wi-Fi password: `{"password":"x"}`, a quoted value continued at a lower indentation, `? key` / `: value`, a flow mapping's value on the next line. | medium | Each is read (`7d8fec1`); a key after `[`, and a line that is only a flow sequence, too (`a5078a6`). |
| one-line keys (K3) | A private key on one line under a secret key was shown whole. | medium | Its body is hidden and the line's key rules still run (`7d8fec1`); a body that begins on the BEGIN line, too (`a5078a6`). |
| NetworkManager (K4) | `mka-cak`, `802-1x.pin` in netplan's passthrough, and vpnc's keys with blanks were shown. | medium | Recognised (`7d8fec1`). |
| PGP, marker text (K5) | PGP private key blocks were shown, and so was any value that began with `<hidden`. | low | Both hidden. Each rule now finds its spans in the line as it is, so no rule reads another's marker (`7d8fec1`). |
| the note (K6) | `sc diff ID` said nothing on stderr when only the file on disk hid values. | low | One note for each path that hid anything (`0df7f46`). |
| diff numbers (K7) | `<hidden N>` told which hidden values were equal: two accounts with one hash, one key reused. | low | Your pick: `<hidden>` for a value the other version hides too, else `<hidden, old>` or `<hidden, new>` (`0df7f46`). |
| validator lines (K8) | A short value was replaced inside other words, where the marker's place told it (a PIN inside a file name); a value a validator printed unquoted or unescaped was missed. | low | A value under 8 bytes is hidden only where it stands alone; values are also matched unquoted and unescaped (`7d8fec1`). |
| sc scope (K9) | It said "stored" for a secret path scd does not record. | low | For such a path it says what holds if `sc snapshot` saves it (`a5078a6`). |
| over-hiding (K10) | A key named in a YAML comment hid every line indented after it, so a diff of an address hid the change. | low | A key in a comment hides its own value and leaves nothing open (`7d8fec1`). |
| a test helper (K11) | `secretEnv` put back a weaker rule than the one it replaced. | cleanup | It puts back the old one; the real rule has a test (`c404bd1`). |
| tests (K12) | 21 of 52 mutations survived. | low | Tests for each, re-anchored on today's code (`a5078a6`). |
| docs (K13) | `progress.html` still said `sc cat` prints a version exactly; nothing warned that `sc cat ID > FILE` writes markers into a secret file. | low | Fixed and warned, in `sc cat --help` and the README's rescue part (`a5078a6`). |

## Left as it is

- `sc diff` shows an old value as `<hidden>` when the new version still holds it anywhere,
  so a reader learns that it still exists, for example on another account. That is what
  your K7 pick asked for.
- `scripts/accept-m4.sh`, the lab's `facts.sh` and `lab/e2e.py` (checks 1.9 and 4.6)
  compare the sha256 of `sc cat` with `/etc/fstab`, which is now a secret file. That holds
  while the fstab has no secret option, as on this VM and in the lab. `--show-secrets`
  would remove the assumption, but in the lab's `--deb` upgrade an older `sc` may run
  those commands; chunk L, which reworks that mode, is the place to settle it.
- `store.Restore`'s refusal of a directory says "see it with: sc cat ID", which for a
  secret file shows markers.

- A mapping under a secret key is hidden whole (a cloud-init `chpasswd:` block, its
  `expire:` included), as the package doc says: it errs towards hiding.
- Only the files the scope names are hidden. Content-based detection was not built; it
  must be settled before M7 sends diffs to a model.

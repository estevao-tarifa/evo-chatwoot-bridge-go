# Open conflicts between sources

Cross-source contradictions found while building the Phase 1 contracts. Each
entry: the conflicting claims, the evidence, and how we resolve it. Nothing here
is resolved by guessing — items stay `[needs live validation]` until the Phase 7
pilot, and the Go implementation must pick the **safe** side meanwhile.

## C1 — Does Chatwoot protect `phone_number` against duplicates? (affects H2)

- **chatwoot-specialist** (`docs/contracts/chatwoot/`): "Chatwoot enforces
  per-account uniqueness on `phone_number`/`identifier`, so race losers get 422."
- **researcher** (Chatwoot 4.18.0 source): only
  `identifier`/`email` have DB unique indexes; **`phone_number` has none** →
  concurrent creation can produce duplicate phone contacts.

**Resolution — VERIFIED against production (Chatwoot 4.12.1, read-only).**
`identifier` has a real UNIQUE index (`uniq_identifier_per_account_contact`),
but `phone_number` has only a non-unique index; the model validates phone
uniqueness (`contact.rb:54-56`) yet there is no atomic DB guarantee. → Treat
`phone_number` as NOT protected: serialise contact creation per
(account, identity) in the bridge and rely on `contacts_map` for reuse.
See the production reconnaissance report.

## C2 — Is `lock_to_single_conversation` a server-side reuse guarantee? (affects H2/H5)

- **chatwoot-specialist**: `createConversation` without `source_id` creates a new
  `contact_inbox` each call → `lock_to_single_conversation` is effectively a
  no-op; reuse is entirely the bridge's responsibility.
- **researcher** (4.18.0): `lock_to_single_conversation` = server-side reuse.

**Resolution (safe side):** never rely on Chatwoot for reuse. The bridge always
resolves the conversation itself (G0) and only creates when no usable candidate
exists. Live test in Phase 7.

## C3 — `message_created` sender typing for contacts

- **researcher** (source): contact senders have **no `type` key** in webhooks
  (docs claim `"contact"`).
- Legacy bridge filters only `message_type === "incoming"` and
  `sender.type === "agent_bot"`, so this is currently harmless — but any new
  filter must not assume `sender.type === "contact"` exists.

**Resolution:** key off `message_type`/`sender.id` per the legacy filters, never
off a `sender.type === "contact"` check. Live payload capture in Phase 7.

## C4 — Evolution GO `subscribe[]` casing / endpoint existence (affects webhook bootstrap)

- Legacy bridge sends a PascalCase subscribe list (`Message`, `QrcodeUpdated`, …)
  and calls `/chat/markAsRead` + `/chat/getBase64FromMediaMessage/{instance}`.
- **researcher**: upstream GO builds expect **UPPERCASE, case-sensitive**
  constants (`MESSAGE`, `SEND_MESSAGE`, …); empty list → MESSAGE only; unknown
  entries silently discarded. `/chat/markAsRead` and
  `/chat/getBase64FromMediaMessage` exist in **no** public GO release
  (`/message/markread`, `/message/downloadmedia` are the real ones).

**Resolution (updated 2026-10-11, ADR-0008):** the deployed server may be a
non-public build `[needs live validation]`. The Go client uses the source-proven
GO mark-read path **and** GO body together; an unverified custom path is rejected
**without I/O** rather than paired with a guessed/legacy body. Media download is
disabled without I/O until a source-proven full-message contract exists. The
subscribe list stays configurable. A 404 does not prove endpoint absence.

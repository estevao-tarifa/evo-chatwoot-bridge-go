# Contract — JID / LID / groups semantics

Scope: how the legacy bridge treats WhatsApp JIDs and LIDs, how group identity
and names are resolved, broadcast/newsletter filtering, and mention resolution —
with `file:line` evidence and the failure modes behind `AGENTS.md` §7. Sources:
legacy commit `daeb04b` (v5.4.4), Evolution GO `0.6.1` (`89eaae337e61`) /
`main` (`9337afc47e10`), whatsmeow `types` (linked via Evolution GO), official
docs. Synthetic values only.

## 1. JID taxonomy seen in payloads

| Form (synthetic) | Meaning | Typical payload slot |
|---|---|---|
| `5511900000001@s.whatsapp.net` | phone-number JID (PN); 1:1 chat | `Info.Chat`, `key.remoteJid` |
| `5511900000001:38@s.whatsapp.net` | device-qualified PN | `Info.Sender` (docs example shows `:38`) |
| `100000000000001@lid` | LID (WhatsApp alternate user id) | `Info.SenderAlt`, `mentionedJID[]` |
| `120363000000000001@g.us` | group | `Info.Chat`, `data.groupData.JID`, `data.JID` |
| `status@broadcast` | status/stories | `Info.Chat` |
| `5511900000001-1234567890@broadcast` | broadcast list | `Info.Chat` |
| `120363000000000002@newsletter` | channel/newsletter | `Info.Chat` |

Producer evidence: `Info.*` fields are whatsmeow `types.MessageInfo`
(`SenderAlt` documented in Evolution GO webhook docs; swap logic in
`0.6.1/pkg/whatsmeow/service/whatsmeow.go:1069-1106`); camelCase payloads use
`key.remoteJid` / `key.participant` (`server/v4.2-rewrite.test.ts:331-354`).

## 2. Bridge extraction rules

`server/services/bridgeProcessor.ts`:

| Function | Rule | Line |
|---|---|---|
| `extractPhone(jid)` | `jid.replace(/@.*$/, "")` — strips everything from the first `@`; **keeps** a `:device` suffix | 149-151 |
| `isGroupJid(jid)` | `jid.includes("@g.us")` (substring, not suffix) | 153-155 |
| `contactIdentifier` | group → full `chatJid` (`…@g.us`); 1:1 → `extractPhone(chatJid)` | 542-546 |
| `senderPhone` | group → `extractPhone(senderJid)`; 1:1 → `extractPhone(chatJid)` | 548-550 |
| validation | `contactIdentifier.length >= 5`; 1:1 sender phone must not contain `@` and be ≥ 5 chars | 552-560 |
| `displayName` | group → `groupName \|\| "Grupo"`; 1:1 → `pushName \|\| senderPhone` | 562-565 |
| `senderName` (group prefix) | `pushName \|\| senderPhone`; message prefixed `*name:*\n` | 567-568, 652-654 |

Chatwoot mapping (`server/services/chatwootClient.ts`):

- Contact key = `contactIdentifier`; Chatwoot search: groups match
  `identifier === phone`, 1:1 matches digits-containment of `phone_number`
  (`:63-79`).
- Group contact creation: `{name, identifier: <group JID>}` — no phone field
  (`:82-91`). 1:1 creation: `phone_number` normalized with `+`, `identifier`
  digits, fallback name = digits (`:92-102`).
- `findOrCreateContact` updates the name whenever the payload name differs
  (`:105-116`).
- `upsertContactMap` stores `whatsappPhone = key` (group JID for groups) and
  `whatsappName = displayName` (`bridgeProcessor.ts:624-630`).
- Conversation creation posts only `contact_id`, `inbox_id`, `status`
  (`chatwootClient.ts:134-141`) — the legacy code never sets a conversation
  title, so Chatwoot derives it from the contact name. Rule “title never a JID”
  therefore depends on the contact name rules above (`AGENTS.md` §7 rule 5).

Group identity is therefore stable (`@g.us` JID, scoped by connection via
`contacts_map.connectionId`), which satisfies rule 3 structurally; the failure
mode is **name quality**, not identity (see §4).

## 3. LID semantics

1. `Info.SenderAlt` carries the LID alternate of the sender
   (Evolution GO webhook docs, "Alternative sender JID (LID)").
   **The bridge never reads `SenderAlt`** — not in `parseInfo`
   (`bridgeProcessor.ts:201-216`) nor anywhere else (grep evidence: only
   `resolveMentions` handles `@lid` strings from `mentionedJID`).
2. Evolution GO performs a JID swap **before** emitting the webhook: when
   `Info.Sender` is `@lid` and `Info.SenderAlt` is `@s.whatsapp.net`, Sender and
   Chat become the PN and `SenderAlt` becomes the LID
   (`0.6.1/.../whatsmeow.go:1069-1106`; same in `main`). Consequence: the bridge
   normally sees a PN in `Chat`/`Sender` and never needs LID resolution.
3. `[needs live validation]`: if a producer emits a 1:1 chat addressed by LID
   without that swap, the bridge keys the contact by the **LID digits**
   (`extractPhone("…@lid")`), colliding with the intention of keying by phone —
   a duplicate-identity path. Question for Phase 7: does the deployed version
   ever deliver `Info.Chat` as `@lid` (e.g. for hosted LIDs)?

## 4. Group names — where `"Grupo"` and overwrites come from (H3)

- `groupName` has exactly one source: `data.groupData.Name || data.groupData.name`
  (`bridgeProcessor.ts:212`).
- `displayName = groupName || "Grupo"` (`:563-564`).
- Contact creation default: `name: name || "Grupo"` (`chatwootClient.ts:86`);
  update path always overwrites when the incoming name differs
  (`chatwootClient.ts:109-113`).
- `upsertContactMap` writes `whatsappName = displayName` over any previous value
  (`bridgeProcessor.ts:624-630`).
- Payloads with **no `groupData`**:
  - standard Evolution API camelCase events never carry it (fixture
    `server/v4.2-rewrite.test.ts:331-354`; official API docs list no group
    metadata in `MESSAGES_UPSERT`),
  - Evolution GO `SendMessage` echoes never carry it
    (`0.6.1/pkg/sendMessage/service/send_service.go:2224-2244`),
  - Evolution GO `Message` events omit it whenever `GetGroupInfo` fails
    (`0.6.1/.../whatsmeow.go:1470-1475`; `main` same at `:1596-1602`).
- Therefore any of those payloads for a group whose mapping/contact exists will
  push name `"Grupo"` (or the sender phone in some paths) into Chatwoot and into
  `contacts_map` — the regression documented as H3 in
  `HYPOTHESES.md` and the target of `AGENTS.md` §7 rule 4.
- Regression fixtures already exist: `server/v5.3-fixes.test.ts:327-399`
  (group name vs "Grupo" vs phone) and `server/v4.2-rewrite.test.ts:293-312`.

## 5. Mention resolution (LID → phone)

`resolveMentions` (`bridgeProcessor.ts:223-263`) — applied only when parsing
`extendedTextMessage.text` (`:366-372`):

1. Read `extendedTextMessage.contextInfo.mentionedJID` (fallback
   `mentionedJid`); no mentions → text unchanged (`:230-234`).
2. Build map from `data.groupData.Participants` (`Participants ||
   participants`), per participant: `lid = extractPhone(p.JID || p.LID)`,
   `phone = extractPhone(p.PhoneNumber)` (`:236-245`).
   whatsmeow shape: `GroupParticipant{JID, PhoneNumber, LID, DisplayName, …}`
   where `JID` always equals either the LID or the PN
   (whatsmeow `types/group.go:102-120`); flattened JSON via embedded
   `GroupName`/fields shows `JID`, `LID`, `PhoneNumber`, `DisplayName`
   (fixtures `server/v5.3-fixes.test.ts:184-322`).
3. Replace every `@<lid>` with `@<phone>`; unmapped mentions keep the LID
   (`:247-260`; test `v5.3-fixes.test.ts:250-275`).

Limitations:

- Requires `groupData`; without it (camelCase payloads, failed `GetGroupInfo`,
  `SendMessage` echoes) mentions stay as `@<LID>`.
- Mentions inside image/video/document captions or plain `conversation` are
  **not** resolved (only `extendedTextMessage`).
- The map key prefers `p.JID`; if `JID` is a PN JID, `phone === lid` and the
  replacement is skipped — safe.

## 6. Broadcast / newsletter / status filtering

`bridgeProcessor.ts:532-540` (commit `v5.4.4`):

```ts
if (info.chatJid.includes("@broadcast") || info.chatJid.includes("@newsletter")) return;
```

- Covers `status@broadcast`, `…@broadcast` lists (`v5.3-fixes.test.ts:543-604`)
  and `…@newsletter` (`:550-554`).
- Applied after the `isFromMe`/cache check and connection lookup; a dropped
  event is not logged as a message (log line only).
- Group JIDs and normal 1:1 JIDs are not affected (tests `:556-566`).
- Server-side defence exists in Evolution GO (`EVENT_IGNORE_STATUS`,
  `EVENT_IGNORE_GROUP` env, default `false`) — the bridge cannot rely on it.

## 7. Sharp edges (for Go parser tests)

| Edge | Behaviour | Evidence |
|---|---|---|
| `Sender` with device suffix `:38` | `extractPhone` returns `5511900000001:38`; used as n8n `phone` and group-name fallback, not as a send target for 1:1 | `:149-151`, `:638-639` |
| `@g.us` substring test | any JID containing `@g.us` is a group | `:153-155` |
| Group send target | `contacts_map.whatsappPhone` holds the full group JID; `/send/*` accepts JIDs and skips the user-exists check for `@g.us` | `bridgeProcessor.ts:624-630`; `0.6.1/.../send_service.go:384-425` |
| 1:1 `contactKey` | phone digits (from chatJid), not the sender JID | `:542-546,609` |
| `isFromMe` 1:1 contact name | `null` passed deliberately so an existing contact is not renamed with our own push name | `:610-615` |
| Group sender name in Chatwoot | `*PushName:*` prefix inside the message body | `:652-654` |

## 8. Open questions (`[needs live validation]`)

1. Do LID-addressed 1:1 chats ever reach the bridge without the Evolution GO
   swap (`Info.Chat = …@lid`)? If yes, contact keys collide with PN contacts.
2. Does the pilot enable `EVENT_IGNORE_STATUS/GROUP` server-side, or is client
   filtering the only defence?
3. `[unverified]` whether `groupData.Participants` always contains
   `PhoneNumber` for LID participants on the pilot's WhatsApp version.

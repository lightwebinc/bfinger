# Flows

Every operation as a diagram, in the order things happen: a reader resolves a
name and checks an answer, an owner publishes, updates, rotates and retires, a
host admits and joins, and money moves. The rules are in
[committed-record.md](committed-record.md); the user's view is
[user-guide.md](user-guide.md). If you read one diagram, read
[4. Verification order](#4-verification-order).

| Letter | What it is |
| --- | --- |
| **S** | the record: the owner's claim, a few hundred bytes of CBOR |
| **K** | the carrier: the transaction that holds S in its output 0, never mined |
| **C** | the commitment: K's txid, the only thing about S that reaches the chain |
| **O** | the state token: a mined transaction carrying `[tag, C]`; each update spends the last |
| **F** | the funding tree: one mined transaction whose outputs the carriers spend, one each |
| **w**, **wc** | the witness, a secret the owner keeps, and `wc = SHA-256(w)`, which the record publishes |

**SEAM** marks what is designed and not built: the BRC-52 handle certificate
check, a messagebox for payment notices, delegation, the `grants` / `links` /
`media` sub-stores, hooks, host-side charging, and a reader-side spend check.
Keys, txids, addresses and host names below are illustrative.

## Reading

### 1. Resolution: an address to an identity key and a host

```mermaid
flowchart TD
  ARG["the argument"] --> ISKEY{"66 hex beginning 02 or 03"}
  ISKEY -->|"yes"| NEEDHOST{"a host is configured"}
  NEEDHOST -->|"no"| E1["usage error, exit 2, nothing dialled"]
  NEEDHOST -->|"yes"| HOST["the host base URL"]
  ISKEY -->|"no"| ACCT["parse: lowercase, strip acct: or a leading @, split off a +tag"]
  ACCT --> MAN["GET https://example.com/manifest.json"]
  MAN --> HANDLES{"metanet.handles present, version major 1"}
  HANDLES -->|"no"| E2["error, exit 2; the well-known path is not probed"]
  HANDLES -->|"yes"| RES["GET the resolve endpoint, https only"]
  RES --> GONE{"410, or revoked true on a 200"}
  GONE -->|"yes"| E5["error, exit 2, noting any forwarding record"]
  GONE -->|"no"| ECHO{"the answer echoes the handle and domain asked"}
  ECHO -->|"no"| E4["error, exit 2: one static file answering for everyone"]
  ECHO -->|"yes"| KEY["identityKey; the handle certificate is kept, NOT verified (SEAM)"]
  KEY --> PICK{"a host flag or config value is set"}
  PICK -->|"yes"| HOST
  PICK -->|"no"| OVL["metanet.overlays.ls_finger from the manifest"]
  OVL --> HOST
```

The manifest says which endpoint resolves handles and which host serves the
identity; the resolve endpoint says which key. The echo check catches a static
file that passes every other check. A bare identity key skips both requests,
so it needs `-host`. Both requests are HTTPS only, system roots, TLS 1.2
minimum, no environment proxy, same-origin redirects, 256 KiB bodies.

### 2. Header source

```mermaid
flowchart TD
  CFG["header_url"] --> SET{"set"}
  SET -->|"no"| U["usage error, exit 2, before any request"]
  SET -->|"yes"| P{"its form"}
  P -->|"woc:main or woc:test"| W{"matches the network key"}
  W -->|"no"| U
  P -->|"anything unparseable"| U
  W -->|"yes"| HF["GET the header at the height a proof claims"]
  P -->|"chaintracks:URL"| HF
  P -->|"an http or https URL"| NB["a bridge: GET /v1/root/HEIGHT, roots only, taken as given"]
  HF -->|"404"| NO["root not held: REFUSED-BUMP"]
  HF -->|"other failure"| ERR["ERROR, exit 2"]
  HF -->|"200"| POW{"height echoed, the 80 bytes hash to the claimed hash, the hash meets its own target, the target meets the network floor"}
  POW -->|"no"| ERR
  POW -->|"yes"| CMP{"its merkle root equals the proof's"}
  NB -->|"404"| NO
  NB -->|"other non-200"| ERR
  NB -->|"200"| CMP
  CMP -->|"no"| NO
  CMP -->|"yes"| OK["the proof holds"]
```

`network` is `main` (default), `test` or `regtest`. On `main` the floor is
difficulty 4e9, so a lying WhatsOnChain or chaintracks source must mine a real
block; `test` and `regtest` have no floor. A `woc:` source that names another
network, or a spec that does not parse, fails every command at startup with
exit 2. The same check runs for `receive` and `fund -txid`.

### 3. The read path, end to end

```mermaid
sequenceDiagram
  participant R as Reader
  participant D as Domain
  participant H as Overlay hosts
  participant S as Header source
  R->>D: GET the manifest, then the resolve endpoint
  D-->>R: the host, and the identity key to expect
  R->>H: POST /lookup with that key, at quorum hosts
  H-->>R: an output-list of BEEF objects from each
  Note over R,H: different counts are asked again up to 4 times, 3 s apart. Answers that still differ are REFUSED-FORK
  R->>S: the root for each height a proof claims
  S-->>R: the root, or a header checked for its work
  Note over R: diagram 4 runs on bytes already in hand
  R->>R: write the pin store, the only irreversible local action
```

Each party answers one question. The header source is the one answer the
reader cannot check for itself, so an unreachable source is ERROR (exit 2),
never a refusal.

### 4. Verification order

```mermaid
flowchart TD
  T0{"a chain tracker was supplied"} -->|"no"| ERR["ERROR, exit 2"]
  T0 -->|"yes"| T1{"the host answered any outputs"}
  T1 -->|"none"| NOTOK["NO-TOKEN"]
  T1 -->|"some"| T2["parse each BEEF and classify the output it names"]
  T2 --> D1{"the BEEF parses, the index is inside the outputs, and it is a token or a carrier record output"}
  D1 -->|"no"| DEC["REFUSED-DECODE"]
  D1 -->|"yes"| T3{"how many state tokens"}
  T3 -->|"none"| NOTOK
  T3 -->|"two or more"| FORK["REFUSED-FORK, every token txid listed"]
  T3 -->|"exactly one"| T4{"SPV the token against the reader's own headers"}
  T4 -->|"the proof is not in the header source"| BUMP["REFUSED-BUMP"]
  T4 -->|"a script or an ancestor failed"| DEC
  T4 -->|"the header source could not answer"| ERR
  T4 -->|"passes, mined or through its ancestry"| T5{"a carrier whose txid equals the token's C was served"}
  T5 -->|"no"| PEND["RECORD-PENDING"]
  T5 -->|"yes"| T6{"SPV the carrier through its funding parent"}
  T6 -->|"the funding parent is not proven"| BUMP
  T6 -->|"the input does not satisfy the funding output"| SIG["REFUSED-SIG"]
  T6 -->|"an ancestor is missing"| DEC
  T6 -->|"the header source could not answer"| ERR
  T6 -->|"passes"| T7{"validate the carrier on its own"}
  T7 -->|"it could be mined"| MIN["REFUSED-MINEABLE"]
  T7 -->|"the lock is not the record derivation"| KD["REFUSED-KEY-DERIVE"]
  T7 -->|"the field signature fails"| SIG
  T7 -->|"any other reason"| DEC
  T7 -->|"passes"| T7B{"the record's identityKey parses as a public key"}
  T7B -->|"no"| DEC
  T7B -->|"yes"| T8{"the token is locked to derive(identity, profile)"}
  T8 -->|"no"| KD
  T8 -->|"yes"| T9{"the token's field signature verifies under that key"}
  T9 -->|"no"| SIG
  T9 -->|"yes"| T10{"the record names the identity the domain resolved"}
  T10 -->|"no"| KEY["REFUSED-KEY"]
  T10 -->|"yes"| T11{"the pin"}
  T11 -->|"marked retired"| RET["REFUSED-RETIRED"]
  T11 -->|"absent"| FC["first contact"]
  T11 -->|"the same key"| OKPIN["the pin holds"]
  T11 -->|"a different key"| T12{"the previous carrier is a rotation by the pinned key naming this key, and it validates"}
  T12 -->|"no"| KEY
  T12 -->|"yes"| ROT["a verified rotation, the caller re-pins"]
  FC --> T13{"the sequence is at or above the pinned one"}
  OKPIN --> T13
  ROT --> T13
  T13 -->|"below"| SEQ["REFUSED-SEQ"]
  T13 -->|"ok, and the record is a create"| T16{"the validity window, with a 120 second skew"}
  T13 -->|"ok, any other kind"| T14{"the previous carrier was served"}
  T14 -->|"no"| COM["REFUSED-COMMIT"]
  T14 -->|"yes"| T15{"seq is one past it, the revealed witness hashes to its wc, and the identity continues or it is the rotation to this key"}
  T15 -->|"the sequence does not follow"| SEQ
  T15 -->|"the witness does not hash"| WIT["REFUSED-WITNESS"]
  T15 -->|"the identity does not continue"| KEY
  T15 -->|"all three hold"| T16
  T16 -->|"outside"| EXP["REFUSED-EXPIRED"]
  T16 -->|"inside, the token is mined"| VER["VERIFIED"]
  T16 -->|"inside, the token is unmined"| VU["VERIFIED-UNMINED"]
```

Signatures and derivations are settled before the pin is consulted, so the
reader never decides who should have signed and then looks for a matching
signature. A header source behind the chain lands on REFUSED-BUMP, not on a
wrong verdict. Exit codes: 0 for VERIFIED, 1 for every refusal, 2 for ERROR;
VERIFIED-UNMINED exits 0 for a lookup and 1 under `verify` (`-accept-unmined`
overrides). Deliberately not checked: that the token spends the previous
token, that the previous carrier is itself proven, that a retirement is
terminal, and anything about `refs`.

### 5. The pin store

```mermaid
stateDiagram-v2
  [*] --> Absent
  Absent --> Active: first contact accepted at a terminal or with -yes
  Absent --> Absent: first contact declined, nothing written
  Absent --> Active: keys trust -key
  Active --> Active: a mined answer advances the sequence, an unmined one keeps it
  Active --> Active: a verified rotation files the old key as rotated-from
  Active --> Retired: the answer is a retirement record
  Retired --> Retired: every later lookup refuses REFUSED-RETIRED
  Active --> Absent: keys forget
  Retired --> Absent: keys forget
  Active --> Active: keys trust -force
  Retired --> Active: keys trust -force
```

Without the pin a host could swap the key it answers with and every signature
would still verify. Nothing is written unless the run passed; the file is
written to a temporary file, fsynced, renamed, and the directory fsynced. A
`@rotated-from` line never satisfies a pin. A first contact that lands on a
retirement writes an ACTIVE pin, so the refusal appears two lookups later.

## Objects

### 6. Object shapes

```mermaid
flowchart LR
  subgraph tok["State token, mined"]
    TK1["output 0, PushDrop, lock before, 1 satoshi"] --> TK2["field 0 is the tag 62 66 01, field 1 is C, 32 bytes"] --> TK3["field 2 is the DER signature over sha256 of tag and C"] --> TK4["locked to derive(identity, profile)"]
  end
  subgraph car["Carrier, never mined"]
    CR1["output 0, PushDrop, lock before, the funding output's value, fee zero"] --> CR2["field 0 is S, canonical CBOR"] --> CR3["field 1 is the DER signature over sha256 of S as pushed"] --> CR4["locked to derive(identity, record)"]
  end
  subgraph fnd["Funding output"]
    FD1["key, OP_CHECKSIG, tag 62 66 02, OP_DROP"] --> FD2["one field, NO signature"] --> FD3["locked to derive(identity, record)"]
  end
  FD3 -->|"spent by the carrier's input, which SPV checks"| CR1
  CR1 -->|"the carrier's txid is C"| TK2
```

Each field signature covers only its own object. The carrier's input
signature is a second proof of authorship that SPV checks for free. The
funding tag lets a host admit these outputs without admitting every
pay-to-public-key, and admitting them is what makes the kill visible.

### 7. The commitment

```mermaid
flowchart LR
  S["S, canonical CBOR, body at most 16384 bytes"] --> K["the carrier pushes S into output 0 and signs it"]
  K --> C["C is the carrier's txid, SHA-256d, in HASH byte order"]
  C --> O["the token pushes the tag and C, and is mined"]
  S --> SALT["a 32 byte salt in S keeps C hiding"]
  S --> PREV["S.prev is the previous carrier's C: the chain of records"]
  O --> SPEND["the token spends the previous token: the chain of tokens"]
  S --> REFS["refs: a Merkle root per sub-store, with its head member named"]
```

C is the hash byte order, not the display hex; a reader that reverses it
finds no carrier. A store with one member has that member as its head; with
more, the head is a manifest listing the members, and the reader rebuilds the
root from it before fetching any.

## Transitions

### 8. Create

```mermaid
flowchart TD
  WAL["a spendable output in the wallet"] --> TREE["mint a funding tree: 16 outputs of 1 sat by default, never fewer than the transition spends, change to the fund key"]
  TREE --> SET1["settle it: wait for its proof, or with proofs = async for acceptance only"]
  SET1 --> PUB1["record it in state.json, then POST it to the topic"]
  PUB1 --> REC["build the record: seq 1, kind create, prev all zero, wc = sha256 of a fresh witness"]
  REC --> CAR["mint the carrier: spends the next funding output, input sequence 0, nLockTime 4102444800, fee zero"]
  CAR --> TOK["mint the token: output 0 is the tag and C, one fee input, then change"]
  TOK --> J["write the journal entry, before either leg"]
  J --> SET2["settlement leg: the token"]
  SET2 --> OBJ["object leg: the carrier's atomic BEEF, then the token's"]
  OBJ --> ST["save state.json, including the new witness"]
```

The tree is published so hosts hold its outputs; a host that never admitted
it is never told it was swept. `-store` sub-records are minted first from the
same tree. State is saved once the token is settled, before any object is
posted, so a failed post is recovered by `publish -resume`. Without `-yes`
everything is built and printed and nothing is sent (with `funding = wallet`,
refused instead).

### 9. Update

```mermaid
flowchart LR
  subgraph pre["Current"]
    PC["carrier n-1, commitment C, wc"]
    PT["token n-1"]
  end
  subgraph cur["This transition"]
    NR["record n: prev is that C, prevWitness reveals the secret behind wc, a fresh wc"]
    NC["carrier n spends the next output of the same tree"]
    NT["token n spends token n-1, plus a fee input"]
  end
  PC -->|"named by prev"| NR
  NR --> NC
  NC -->|"its txid is the new C"| NT
  PT -->|"spent by"| NT
  NR -->|"sha256 of prevWitness equals the previous wc"| PC
```

Both chains advance at once: records through `prev` and the witness, tokens
through the spend. The next record needs the spend key and a secret the
previous record committed to. Rotate adds a successor; retire carries an empty
body.

### 10. Rotation

```mermaid
sequenceDiagram
  participant O as Owner
  participant F as Home files
  participant P as Plane and host
  participant D as Domain
  participant R as Reader
  O->>F: rotate writes successor/identity.json
  O->>P: a kind 3 record, signed by the CURRENT key, naming the successor
  O->>F: state.json records the pending successor
  Note over R: readers still see the current key
  O->>P: the next transition, signed and locked under the SUCCESSOR
  Note over O: the rotation token is spent with the predecessor's key, and a new funding tree is minted under the successor
  O->>F: identity.json becomes identity-prev-N.json, the successor file takes its place
  Note over R: the record names the successor, the domain still answers the predecessor, so every reader refuses REFUSED-KEY
  O->>D: serve the printed resolve document naming the successor
  R->>P: the next lookup
  P-->>R: the rotation carrier alongside the new state
  Note over R: the old key is filed as rotated-from, the new key becomes the pin
```

A reader re-pins only because the rotation carrier is served and validates on
its own. The predecessor's key file is kept because the wallet still holds
outputs locked to its fund key. A crash between the two renames leaves no
`identity.json`; do not run `bfinger init` (the next promotion would
overwrite the kept predecessor file), move the successor file into place by
hand.

### 11. Retire and kill

```mermaid
flowchart TD
  subgraph ret["Retire: a record that says stopped"]
    R1["kind 4, empty body, prev and prevWitness present"] --> R2["published like any transition"] --> R3["the host marks the state terminal"]
    R3 --> R4["the host still answers it, and refuses later transitions, reason retired"]
    R3 --> R6["a reader verifies it, exits 0, marks the pin retired"] --> R7["its NEXT lookup refuses REFUSED-RETIRED"]
  end
  subgraph kil["Kill: a spend that makes the records unservable"]
    K1["one mined sweep per funding tree, every output, used or not"] --> K2["output 0 is a funding-shaped tombstone"] --> K3["every carrier from that tree is now a double spend"]
    K3 --> K4["hosts drop every state those carriers are part of"] --> K5["lookup answers nothing, pending included"] --> K6["a reader gets NO-TOKEN"]
  end
```

A retirement verifies and stays readable; a kill removes the ability to serve
anything, whoever holds a copy. A reader does not enforce terminality: an
update after a retirement verifies, and the host and the `@retired` pin stop
it.

## Publishing

A transition needs a submit endpoint (`facade`), a settlement leg (`settle`:
`tcp:`, `rpc:` or `arcade:`) and, except in the case of diagram 13, a node
(`rpc`, `asset`). None has a default; a missing one is a usage error before
anything is sent. `publish -resume` needs only a facade, `doctor` nothing,
reading none of them.

### 12. The two legs of a transition

```mermaid
flowchart TD
  TX["one signed token, and two BEEF objects"] --> L1["settlement leg"]
  TX --> L2["object leg"]
  L1 --> EF["tcp: EF in ONE write on a fresh connection, no acknowledgement"]
  L1 --> RPC["rpc: hex to a node, acknowledged, the txid must match"]
  L1 --> ARC["arcade: EF to POST /tx, up to 15 s for the network's verdict, a refusal is an error"]
  EF --> MINE{"proofs"}
  RPC --> MINE
  ARC --> MINE
  MINE -->|"wait"| POLL["poll the node's asset API every 5 s, up to 10 min"]
  MINE -->|"async, not with tcp"| GO["go on, the proof is collected by a later command"]
  L2 --> POST["POST each atomic BEEF to the facade, one x-topics header"]
  POST --> STEAK["a 200 that admits nothing is reported as DUPLICATE"]
  POLL --> STATE["state.json is saved after both objects are posted"]
  GO --> STATE
  STEAK --> STATE
```

The legs never share a connection, and no function in `publish` accepts both
a transaction and a BEEF: the ingress locks a stream's grammar from its first
four bytes, so a BEEF on the settlement socket poisons it. The carrier is
never settled (it is unmineable). A facade 401 or 403 is named as an auth
refusal; any other non-200 fails the leg.

### 13. Publishing through arcade with no node

```mermaid
sequenceDiagram
  participant B as bfinger
  participant S as Header source
  participant A as Arcade
  participant F as Facade and hosts
  Note over B: settle = arcade URL, proofs = async, header_url set, no rpc or asset
  B->>S: the chain tip
  B->>A: GET /tx/TXID for each kept transaction still unproven
  A-->>B: MINED with a merkle path, pending, or REJECTED
  B->>F: republish each newly proven object
  B->>A: POST /tx a funding tree, when one is needed
  A-->>B: accepted, or no verdict within 15 s, and a refusal stops the command
  B->>F: POST the tree's BEEF with its unproven ancestry
  Note over B: build the carrier and the token, write the journal entry
  B->>A: POST /tx the token
  A-->>B: accepted, or no verdict within 15 s, and a refusal stops the command
  B->>F: POST the carrier's BEEF, then the token's
  Note over B: save state.json with the token's BEEF, print accepted, proof pending
```

Any ARC service works. Without a node, `proofs = async` and `header_url` are
required (usage error otherwise). Arcade's proof is held to a node's checks: it
parses through the BUMP guard, names the transaction asked about, and agrees
with the reported height. Change from an unproven parent is held until the
parent proves. `pay` and `kill` wait for a block (polling the ARC service when
there is no node), but only after the payment's notice is written or the
sweep is recorded and posted; a wait that runs out keeps the coin spent and
holds the change until a later command collects the proof. Under
`proofs = wait`, a token whose wait runs out after it was sent continues as
`async` would: saved and published unmined. `funding = wallet` still needs a node, because the wallet
broadcasts through its own service.

## At the host

### 14. Admission: tm_finger

```mermaid
flowchart TD
  IN["a submission on tm_finger"] --> PARSE{"the BEEF parses"}
  PARSE -->|"no"| OTH["reason other"]
  PARSE -->|"yes"| SCAN["each output"]
  SCAN --> TQ{"a three field PushDrop with the tag 62 66 01 and a 32 byte C"}
  TQ -->|"the tag or the size is wrong"| BT["bad-tag"]
  TQ -->|"the field signature fails"| BS["bad-sig"]
  TQ -->|"it verifies"| AT["admit, token"]
  SCAN --> FQ{"one field PushDrop, tag 62 66 02, no signature"}
  FQ -->|"yes"| AF["admit, funding"]
  PARSE --> CQ{"exactly one record output whose record decodes"}
  CQ -->|"none"| NF["nothing finger-shaped"]
  CQ -->|"two, or it does not decode or validate"| BR["bad-record"]
  CQ -->|"yes"| C2{"nLockTime at least 4102444800 and every input non-final"}
  C2 -->|"no"| MN["mineable"]
  C2 -->|"yes"| C3{"the lock equals derive(identity, record)"}
  C3 -->|"no"| BL["bad-lock"]
  C3 -->|"yes"| C4{"the field signature over the pushed bytes verifies"}
  C4 -->|"no"| BS
  C4 -->|"yes"| AC["admit the record output, carrier"]
  AT --> KEEP["admit what passed; every previous coin is retained either way"]
  AF --> KEEP
  AC --> KEEP
  NF --> SP{"nothing admitted and it spends coins the topic holds"}
  SP -->|"yes"| SPD["accepted for its inputs alone, kind spend"]
  SP -->|"no"| NP["not-pushdrop"]
  BT --> ONE["if nothing was admitted: one refusal, the carrier's reason first, else the token's"]
  BS --> ONE
  BR --> ONE
  MN --> ONE
  BL --> ONE
```

Each object is admitted on its own validity, without the previous state. The
token's lock derivation cannot be checked here (the identity key is not in the
token); the join does it. Previous coins are retained even when the spender is
refused, because the coin is spent on chain regardless and the kill depends on
that fact. The `spend` branch admits a sweep that creates nothing the topic
keeps.

### 15. The join: ls_finger

```mermaid
flowchart TD
  A["an admitted output"] --> K{"what it is"}
  K -->|"funding"| IDXF["index the outpoint"]
  K -->|"token with a valid signature"| T2{"its profile key belongs to a killed identity"}
  T2 -->|"yes"| KL["refused, killed"]
  T2 -->|"no"| IDXT["add it to the tokens listed under its C"]
  K -->|"carrier, record, lock and signature re-checked"| C2{"the identity is killed"}
  C2 -->|"yes"| KL
  C2 -->|"no"| C3{"a funding outpoint it spends already has another spender"}
  C3 -->|"yes"| KILL["killed on arrival, never joins"]
  C3 -->|"no"| J
  IDXT --> J{"the carrier is here, and a token under C is locked to derive(identity, profile)"}
  J -->|"either is missing"| PEND["waits, silently"]
  J -->|"tokens, none the identity's"| F3["bad-lock"]
  J -->|"yes"| SUB{"a sub-record or a manifest"}
  SUB -->|"yes"| STORE["kept for carrier questions, never on the chain"]
  SUB -->|"no"| V2{"the record validates"}
  V2 -->|"no"| F2["bad-record"]
  V2 -->|"yes"| V4{"the kind"}
  V4 -->|"create, and the identity has any state"| F4["duplicate-create"]
  V4 -->|"other, and there is no current state"| F5["no-current, schedules a rebuild"]
  V4 -->|"otherwise"| V5{"the current state is terminal"}
  V5 -->|"yes"| F6["retired"]
  V5 -->|"no"| V6{"it already has a successor"}
  V6 -->|"yes"| F7["rotated"]
  V6 -->|"no"| V7{"prev is the current carrier's C"}
  V7 -->|"no"| F8["bad-prev"]
  V7 -->|"yes"| V8{"the revealed witness hashes to the current wc"}
  V8 -->|"no"| F9["bad-witness"]
  V8 -->|"yes"| V9{"seq is one past the current one"}
  V9 -->|"no"| F10["bad-seq"]
  V9 -->|"yes"| V10{"the current token was spent by THIS token"}
  V10 -->|"no"| F11["not-spent"]
  V10 -->|"yes"| V11{"a rotation whose successor already has a chain"}
  V11 -->|"yes"| F12["successor-taken"]
  V11 -->|"no"| CUR["current; any transition waiting on it is retried"]
```

A failure never displaces what is current; it is counted under its reason in
`finger_join_failures_total`. Arrival order does not matter. Anyone may mint a
valid token over a public C, so tokens are kept as a list and the join takes
the one locked to the carrier's identity. A carrier that spends an outpoint
another carrier already claims kills that other carrier. A rotation to a
killed successor stands without handing it the chain. Queries: `identityKey`
(current token, current and previous carrier), `pending: true` (plus this
identity's tokens whose carrier has not arrived), and `carrier` (one carrier
of this identity, given in DISPLAY order); any other member is refused.

### 16. Catching up from a peer

```mermaid
sequenceDiagram
  participant O as Operator
  participant N as New host
  participant P as Peer host
  O->>N: OVERLAY_SYNC_PEERS tm_finger=peer, then POST /admin/startGASPSync
  N->>P: GASP sync of tm_finger
  P-->>N: unspent outputs only: every carrier, each identity's current token, unspent funding outputs
  Note over N: each is admitted by tm_finger and indexed by ls_finger
  Note over N: carriers whose tokens were spent wait silently, each current token has no chain under it and is refused no-current
  Note over N: every no-current refusal restarts a 500 ms timer
  N->>N: the burst settles and rebuildChains runs once
  Note over N: states cleared, carriers walked by ascending seq then C, prev, witness and seq checked, spend link skipped, killed identities stay killed, nothing counted
  N->>N: log ls_finger rebuilt its chains from the index
  Note over N: lookups answer with no restart
```

The walk is `restore`'s, run without reading storage. Kills arrive the same
way: a sweep's tombstone is an unspent topic output, so sync carries the
sweep, and the host reads its spends from the sweep's inputs, whatever order
the graphs arrive in (and again from the stored BEEF on a restart).

### 17. The kill, at the host

```mermaid
flowchart TD
  SW["the sweep is submitted on tm_finger"] --> SP["the engine reports every previous coin spent, BEFORE admitting the sweep's outputs"]
  SP --> Q{"the outpoint names a carrier and the spender is another txid"}
  Q -->|"no"| HOLD["the spender is remembered and judged when the carrier lands"]
  Q -->|"yes"| KILL["kill that carrier, reason funding-spent"]
  KILL --> V["victims: the carrier's identity, and every identity whose current or previous carrier it is"]
  V --> DEL["each victim's state deleted, its profile key remembered, counted once per identity"]
  DEL --> SUC["a successor pointer aimed at a victim is removed"]
  SUC --> DROP["every carrier of a victim dropped, with its tokens and index entries"]
  DROP --> TOMB["the tombstone is admitted as a funding output"]
  TOMB --> ANS["lookup answers nothing, a late object is refused, reason killed"]
```

A rotation's successor is swept with the predecessor, because the rotation
carrier is its current carrier too. The tombstone gives the sweep an admitted
output, which is how the spend is recorded in storage and survives a restart.
A host that never admitted the funding tree is never told of the spend and
keeps answering.

## Payment

### 18. BRC-29 pay and receive

```mermaid
sequenceDiagram
  participant A as Payer
  participant D as Recipient domain
  participant N as Settlement leg
  participant B as Recipient
  A->>D: resolve the address to an identity key
  D-->>A: the identity key
  Note over A: derive a destination under BRC-29 with a fresh prefix and suffix
  A->>N: settle an ordinary payment and wait for its proof, from the node or the ARC service
  A->>A: write the notice a messagebox would carry (SEAM)
  A->>B: hand the notice over out of band
  B->>B: check the protocol, the addressee, the BEEF and the txid
  B->>B: verify the proof against its OWN header source
  B->>B: re-derive the script and compare it with the output
  Note over B: the output joins the wallet with its derivation, spendable at once
```

Nothing about the payment rides the plane. The notice is the only copy of the
derivation; losing it means the output cannot be added.

## Trust

### 19. What is trusted

```mermaid
flowchart TD
  D["the domain"] -->|"which key to EXPECT; the certificate is unverified (SEAM)"| R["the reader"]
  H["the overlay host"] -->|"bytes to check; replicas that disagree are refused"| R
  PL["the plane and the facade"] -->|"delivery only"| R
  CH["the chain"] -->|"ordering, and one spend per output"| R
  HS["the configured header source"] -->|"which root a height commits to; work checked where headers are served"| R
  PN["the reader's own pin store"] -->|"the key it saw last time"| R
```

The reader believes only headers from a source it chose and a pin it wrote,
which is why `header_url` has no default. The pin bounds a domain that starts
answering a different key. SPV cannot say whether an output is spent, so a
reader with no host it trusts cannot see a kill (SEAM).

# Internals

The checking chain with the packages behind each step, the pin store's rules,
a map of the tree, and the dependencies. Formats and normative rules are in
[committed-record.md](committed-record.md); the system's shape is in
[architecture.md](architecture.md).

## The checking chain

A lookup is a sequence of checks, each of which can only refuse, in the
normative order of the specification's section 10. A `bcommon/` path is a
package of the library bfinger imports, `github.com/lightwebinc/bcommon`;
every other path is under `internal/`.

| Step | What is checked | Where |
| --- | --- | --- |
| 1 | The name resolves to an identity key and a host (BRC-169, BRC-180) | `bcommon/resolve/`, `bcommon/hostset/` |
| 2 | The host's answer is an `output-list`, identical at every host asked; each BEEF walked by the guard before the SDK parses it | `bcommon/lookup/`, `bcommon/guard/`, `reader/lookup/`, `reader/verify/`, `cmd/bfinger/reader.go` |
| 3 | The token's merkle proof against the reader's header source, each header's proof of work checked when the source serves headers | `bcommon/headers/`, `bcommon/verify/`, `reader/verify/` |
| 4 | The carrier proven through its funding parent, its txid equal to the token's `C` | `bcommon/verify/`, `bcommon/carrier/`, `protocol/carrier/`, `reader/verify/` |
| 5 | Both key derivations, and both field signatures under them | `bcommon/pushdrop/`, `protocol/token/`, `protocol/carrier/`, `reader/verify/` |
| 6 | The pin for this address, or a first contact | `reader/verify/`, `reader/knownkeys/`, `bcommon/knownkeys/` |
| 7 | Sequence advance, the `prev` binding, and the revealed witness | `reader/verify/`, `protocol/record/` |
| 8 | The validity window, with 120 s skew | `reader/verify/` |
| 9 | Each linked store: the head carrier, the committed root rebuilt, then every member as in steps 4 and 5 | `reader/verify/`, `bcommon/verify/`, `bcommon/store/` |

Step 5 precedes step 6: never decide who should have signed and then look for
a matching signature. The whole order, with every outcome, is
[flows.md 4](flows.md#4-verification-order).

**Where headers come from is a security question.** A reader that takes
headers from whoever answered the lookup has verified nothing, so `header_url`
has no default and a run without it stops before any connection. A bridge
that received headers off the same network is the strongest source; a public
WhatsOnChain or chaintracks source is accepted because each header must carry
its proof of work, at a mainnet floor of difficulty 4e9
([flows.md 2](flows.md#2-header-source)). **Which host answers** is a
separate question: [host-selection.md](host-selection.md).

## Key pinning

An identity key is pinned on first contact; without that, a host that swaps
the key it answers with impersonates anyone and every signature still
verifies. The store enforces:

- A pin is a compressed key (`02` or `03`, 33 bytes) in its one encoding,
  an `x` below the field prime on the curve, written in lower-case hex; an
  uncompressed key, or a compressed one whose `x` is at or above the prime,
  would pin a second spelling of the same point.
- One address has at most one `Active` pin. Retired and superseded pins are
  kept, so a rotation stays auditable.
- A changed key is refused unless a rotation signed by the pinned key names
  it; the old pin then becomes `@rotated-from` history.
- `keys trust` over an existing or retired pin needs `-force`.
- `keys verify` re-runs the chain for every active pin and exits 1 on any
  refusal, unreachable address, rotation or retirement.
- Writes are durable: the temporary file and its directory are both fsynced
  (as for `state.json`, whose witness exists nowhere else).

The file's grammar is in [known-keys.md](known-keys.md).

## The tree

```
cmd/bfinger/        the command, and nothing else
internal/
  config/           settings; no deployment address (header_url, host, facade, settle, rpc, asset) has a default
  goldentest/       the shared vector's reader
  protocol/         bfinger's formats: record, token, carrier, mint
  reader/           lookup questions, the reader's algorithm, the pin file
  publisher/        the publisher's state and its wallet profile
host/               tm_finger and ls_finger in TypeScript, and the finger-host image
deploy/             the self-host compose file
docs/               the specification and these documents
scripts/            third-party licence generation
testdata/golden/    the shared vector every implementation checks, and a Go-only mint vector
testdata/fixtures/  recorded header-source and lookup answers
tools/mintprobe/    the measurement harness behind the frozen choices (its own module)
tools/walletd/      a toolbox BRC-100 wallet on the wire, for wallet = wire (its own module)
```

Nothing under `internal/` is importable: an implementer is better served by
the specification and the shared vector. The generic half is the public
library bcommon (only go-sdk and the standard library below it, tested in its
own repository); bfinger keeps its record, token, wallet profile, pin file
path, questions, reader algorithm and publisher state. The host modules use
bcommon's TypeScript package at the same tag and deploy as one file,
`host/bundle/index.js`, with it inlined
([host/README.md](../host/README.md#deploying)).

| Package | Responsible for |
| --- | --- |
| `protocol/record` | The record schema (`bfr` magic, key table, kinds 1 to 6, body bounds), `Validate()` and its refusals, over `bcommon/cbor` and `bcommon/store`; goldens cross-checked against a second encoder |
| `protocol/token` | The state token: its tags, the `[1,"bfinger"]` protocol, the `profile` and `record` key ids, the 1 satoshi value, the `[tag, C]` layout, over `bcommon/pushdrop` |
| `protocol/carrier` | The record derivation, the funding tag and lock, the record's rules and the classifier, handed to `bcommon/carrier` |
| `protocol/mint` | The token and the funding tree under bfinger's locks, built by `bcommon/mint`, and the BRC-29 payment |
| `reader/lookup` | The finger service name and its two questions (`FingerQuestion`, `CarrierQuestion`), over `bcommon/lookup` and `bcommon/hostset` |
| `reader/knownkeys` | The pin file's default path and header over `bcommon/knownkeys`, and the grammar sample in `docs/known-keys.md` |
| `reader/verify` | The reader's algorithm and its `Options`, driving `bcommon/verify`; the refusal vocabulary re-exported; the conformance matrix in its tests |
| `publisher/owner` | What was last published, the withheld witness, and the funding trees (`bcommon/funding`) |
| `publisher/bwallet` | bfinger's wallet `Profile` (fund protocol, key id, basket, version, legacy `pool.json`) over `bcommon/bwallet` |
| `config` | Settings and their sources in order: flags, `BFINGER_*`, the config file, defaults |
| `goldentest` | Reads `testdata/golden/finger-v1.json`; vector-free helpers are `bcommon/goldentest` |

The bcommon packages bfinger imports:

| Package | Responsible for |
| --- | --- |
| `bcommon/cbor` | The deterministic CBOR subset (RFC 8949) a record is written in |
| `bcommon/store` | Store references and manifests, and the rule that rebuilds a store's root (RFC 6962 arithmetic in `bcommon/commit`, which bfinger's tests also use) |
| `bcommon/pushdrop` | The BRC-42/43 derivation (counterparty anyone, forSelf) and the tagged PushDrop locked and decoded under it |
| `bcommon/carrier` | The unmined carrier, its funding outputs, and the sweep that retracts them |
| `bcommon/mint` | The mined transactions (transition, funding tree, BRC-29 payment) and their fee loop |
| `bcommon/funding` | The funding tree a producer keeps between runs, and rebuilding a transaction kept before it mined |
| `bcommon/producer` | The owner commands' core: fee inputs and change, settlement (waiting, or `proofs = async`), the funding-tree lifecycle, kept unproven transactions, and proof collection that republishes what has mined |
| `bcommon/publish` | The two legs (TCP ingress, node RPC, arcade; the facade), which never share a socket, and the journal |
| `bcommon/nodeapi` | The node: JSON-RPC under a caller-set request id, and the asset API proofs come from, each BUMP checked by `bcommon/guard` before the SDK parses it |
| `bcommon/bwallet` | The embedded BRC-100 wallet (`identity.json`, `wallet.json`), the spendable outputs, and `Signer` over any `wallet.Interface` |
| `bcommon/wirewallet` | The BRC-100 wallet wire: `Dial` for `wallet = wire`, `Serve` for `serve-wallet` |
| `bcommon/termsafe` | The filter every value from someone else's record passes before a terminal, behind `-ansi`, `-ascii` and the `body text` refusal |
| `bcommon/headers` | The chain tracker: a bridge's `/v1` roots, or WhatsOnChain and chaintracks headers checked for proof of work |
| `bcommon/hostset` | Which host answers: selection, failover, quorum |
| `bcommon/lookup` | The BRC-24 client and its `output-list` answer |
| `bcommon/resolve` | Name to identity key through the BRC-180 manifest and BRC-169 resolve endpoint |
| `bcommon/knownkeys` | The pin file's grammar and the pin store's rules |
| `bcommon/verify` | The refusal vocabulary, the SPV verdict against the reader's headers, and `VerifyCarrier` for store members |
| `bcommon/guard` | The walk every BEEF, raw transaction and BUMP from the network passes before go-sdk parses it, bounding each declared count by the bytes present; and `ParsePubKey`, which accepts a key from the wire only in its one encoding |
| `bcommon/goldentest` | Vector-free test helpers: a fixed key, parsing that fails the test, a stub chain tracker |

## Dependencies

Exactly two direct dependencies, asserted by `make deps-check` in CI:
`github.com/bsv-blockchain/go-sdk` at **v1.5.2** and
`github.com/lightwebinc/bcommon` at a tag. The same check asserts that go.mod
and go.sum are tidy, that no module is replaced, that bcommon's own direct
requirements (from its go.mod, and from what its linked packages import) are
exactly go-sdk, and that go-sdk resolves to exactly v1.5.2, since minimal
version selection would otherwise let a bcommon tag move it.

The pin is a security decision: below v1.5.0 the SDK sizes a slice from an
attacker-declared count while parsing a merkle path, so a thirteen byte input
ends the process with an unrecoverable out-of-memory. No dependency bot may
move it. Every BEEF, raw transaction and BUMP bfinger reads from the network
also passes `bcommon/guard` first, so that bound no longer rests on the pin
alone.

The host modules have two runtime dependencies: `@bsv/sdk` at exactly 2.7.1,
which the overlay host provides, and `@lightwebinc/bcommon` at the tag go.mod
pins, vendored as a tarball under `host/vendor/`. A host test fails while the
tarball, the installed package and go.mod disagree. The bundle's only import
is `@bsv/sdk`, and its build fails if any input lies outside `host/src/` and
that package.

# host

`tm_finger` and `ls_finger`: the committed-record topic manager and lookup
service, a module for the reference overlay host (`overlay-blueprints`). The
design is [docs/committed-record.md](../docs/committed-record.md). To run a
host from the `finger-host` image with its database and TLS front, see
[docs/self-host.md](../docs/self-host.md); this file is for loading the module
into a host you run yourself.

## Three object kinds

| kind | shape | admitted when |
| --- | --- | --- |
| token | PushDrop `[ "bf"+0x01, C ]` under the profile derivation, mined | the field signature verifies |
| carrier | PushDrop `[ S ]` under the record derivation, never mined | the record decodes, the lock is the identity's record key, the signature verifies, the transaction cannot be mined |
| funding | PushDrop `[ "bf"+0x02 ]` under the record derivation, no signature | the script is exactly `<key> OP_CHECKSIG <tag> OP_DROP` |

`tm_finger` admits each on its own validity and retains every previous coin a
submission spends, so a carrier's funding output stays in storage marked spent
by the carrier. A transaction that admits nothing but spends outputs the topic
holds is accepted for its inputs alone (counted as `kind="spend"`).

A sub-record (record kind 5) and a store manifest (kind 6) are indexed like any
carrier, answered by the carrier question, dropped by a kill, and never advance
an identity's state. Their body may be 64 KiB, where a transition is bounded at
16 KiB. A host running an older module refuses both, so upgrade hosts before a
publisher uses either.

## Lookups

`ls_finger` joins a token with the carrier it commits to, checks the chain, and
answers three question classes:

| query | answers |
| --- | --- |
| `{ identityKey }` | the current token, the current carrier, and the previous carrier where one exists |
| `{ identityKey, pending }` | the same, plus tokens under that identity whose carrier has not arrived |
| `{ identityKey, carrier }` | one carrier of that identity by its commitment |

A query member the service does not define is refused, never ignored: a
question class is the unit a charge could attach to, and ignoring an extra
member would make a free answer available under a priceable alias. The first
two classes are free on every conforming host
([specification section 14.2](../docs/committed-record.md#142-the-free-base-answer)).
Nothing in these modules meters or prices a query.

## The kill

A carrier spends a funding output. A mined transaction that spends the same
output (the sweep) makes the carrier a double spend and retracts the record.
`ls_finger` keeps `funding[outpoint] -> carrier` and, on a spend by any txid
other than the carrier's, drops every state that carrier is part of, drops the
identity's carriers and tokens, and refuses anything of that identity
delivered later (join refusal `killed`). The carrier's own spend is reported
before the carrier is admitted and is never a kill. Only a host that admitted
the funding tree sees the sweep.

## Restore and catch-up

On start, `restore` re-reads every unspent row, checks each carrier's funding
outpoints (the row's `outputsConsumed`) through `storage.findOutput`, and
kills a carrier whose funding output was spent by another transaction, exactly
as a live spend would. It then replays carriers by sequence (ties by
commitment), checking prev, witness and sequence. The kind is checked first,
so a create-shaped sub-record (kind 5 or 6) that sorts before the real create
cannot take the chain start.

A host that catches up from a peer with GASP receives unspent outputs only:
every carrier (carriers are never spent) but only each identity's current
token, so live joins refuse the history `no-current`. After a burst of those
refusals settles, `ls_finger` re-derives every chain the way `restore` does,
logs `ls_finger rebuilt its chains from the index`, and answers without a
restart. Kills reach such a replica through the sweep, which sync carries
(its tombstone is a topic output): the service reads the spends from every
admitted transaction's inputs, and on restore from each row's BEEF, which
the reference host passes from v0.2.1
([flows 16](../docs/flows.md#16-catching-up-from-a-peer)).

## Deploying

The artefact is one file, `host/bundle/index.js`, built with `make host` (Node
24; `make host-docker` builds and tests inside the Node 24 image). It holds
both modules with `@lightwebinc/bcommon` inlined and imports only `@bsv/sdk`,
by bare specifier. The bundle step fails, and removes the file, if any input
lies outside `host/src/` and that package or the file imports anything else.

Copy the file into the host's tree, beside the host's `node_modules`: Node
resolves a bare import from the nearest `node_modules` above the module file,
so a copy elsewhere either fails to import `@bsv/sdk` or loads a second copy.
Nothing else from `host/` is needed. Two host settings load it:

```
OVERLAY_TOPICS=tm_finger
OVERLAY_MODULES=/app/modules/finger/index.js
```

`OVERLAY_TOPICS` must name `tm_finger` (a module may mount only topics the host
names), and `OVERLAY_MODULES` is an absolute path. The modules have no
configuration of their own. The names `tm_finger` and `ls_finger` are fixed;
a second finger-shaped topic needs a second module built with other names.

The bundle's first line names its versions:

```
// @lightwebinc/bfinger-host <version>: tm_finger and ls_finger, with @lightwebinc/bcommon <version> inlined
```

Modules built on different library versions can share a host, since each
mounts its own topics. A module that cannot be mounted stops the host before
the port opens, naming the path, rather than leaving the topic on the
admit-everything default. A good mount logs:

```
finger module built with bcommon bcommon=<version>
module loaded path=... topics=tm_finger lookups=ls_finger
module lookup restored from storage path=... lookup=ls_finger outputs=<n>
```

**Sizing.** The whole index is in memory, unbounded and without eviction, so
memory scales with the topic. Restore runs before the port opens, with one
storage round trip per funding outpoint of every surviving carrier, so start-up
scales the same way, and a storage failure there refuses the start.

## Metrics to watch

Every series is preset at start-up: a counter that did not exist until its
first event would read as healthy, and a lane refusing everything looks busier
than a healthy one. Alert on ratios, not on the presence of a series.

| Series | Kind | Watch for |
| --- | --- | --- |
| `finger_admitted_total{kind}` | counter | `token`, `carrier`, `funding` or `spend`. A publisher moves `token` and `carrier` together; `spend` is a sweep, so records are being retracted |
| `finger_refused_total{reason}` | counter | `not-pushdrop`, `bad-tag`, `bad-sig`, `bad-record`, `bad-lock`, `mineable` or `other`. Rising `bad-sig` or `bad-lock` against steady admissions is somebody publishing objects that do not verify |
| `finger_join_failures_total{reason}` | counter | `bad-record`, `bad-lock`, `duplicate-create`, `no-current`, `retired`, `rotated`, `successor-taken`, `bad-prev`, `bad-witness`, `bad-seq`, `not-spent`, `killed`. An admitted object failed the chain rules; the previous state stays current |
| `finger_killed_total{reason}` | counter | `funding-spent`. Any movement means an identity was retracted |
| `finger_pending_tokens` | gauge | tokens whose carrier has not arrived. A small number that drains is the delivery window; one that does not drain is a carrier that never arrived |
| `finger_indexed_tokens` | gauge | tokens held |
| `finger_indexed_carriers` | gauge | carriers held |
| `finger_identities` | gauge | identities answerable |
| `finger_killed_identities` | gauge | identities that answer nothing because they were killed |

Watch `finger_admitted_total{kind="token"}` against `finger_pending_tokens`:
tokens arriving while pending climbs means carriers are not being delivered,
which a reader sees as `RECORD-PENDING`.

## Build and test

Node 24: `npm test` builds `dist/` with tsc and the bundle with esbuild, then
runs the tests on `dist/`. `bundle.test.ts` mounts `bundle/index.js` as the
host would and admits the golden token and carrier. `bundle-check.test.ts`
tests the bundle step's refusals on metafiles no clean build produces, because
a bundle with a second SDK inlined still passes the smoke test. The tests read
the Go golden at `../testdata/golden/finger-v1.json`, so both SDKs are checked
against one set of bytes.

The generic half of the modules is `@lightwebinc/bcommon`, the TypeScript
package of the Go library `github.com/lightwebinc/bcommon` at the same tag: the
CBOR codec, store references, the reader's side of the derivation, the field
signature, the funding decode, the carrier core and the engine interfaces,
with test helpers under `@lightwebinc/bcommon/testing`. It carries no finger
constant; `record.ts`, `token.ts`, `keys.ts`, `funding.ts` and `carrier.ts`
supply bfinger's. Its runtime entry imports only `@bsv/sdk`, declared as a peer
at exactly the version this package depends on, so one SDK is installed.

The package is vendored as `vendor/lightwebinc-bcommon-<version>.tgz`, packed
from the library's `ts/` directory at its tag, so installing needs no registry
and the lockfile pins the tarball's bytes. To move tags with the Go side: pack
the package at the tag `go.mod` moves to, replace the tarball, point
`package.json` at it, and run `npm install`. `skew.test.ts` fails while the two
sides name different versions.

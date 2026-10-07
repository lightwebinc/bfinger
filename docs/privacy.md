# Privacy

Who learns what from a publication, a lookup and a payment, each with its
limit. A privacy claim without its limit is worse than none, because somebody
will rely on it. Why the design takes this shape is in
[overview.md](overview.md); the components are in
[architecture.md](architecture.md).

In short: nothing you publish names you on the chain, the record itself is
never written into a block, and reading needs no account, credential or
payment. Everything inside the record is public to every host that carries
it, so write it like a bulletin board entry.

## Who learns what

| Party | Learns | Does not learn | Limit |
| --- | --- | --- | --- |
| A stranger reading the chain | per transition: a derived public key, a three byte tag, a 32 byte hash (the carrier's txid) and a signature | your name, domain, identity key, or any field of the record, or its length | the tokens form one linked chain, so their count and timing are visible |
| Anyone who knows your identity key | every token you have minted, and when | the record, unless they also ask a host | the derivation is fixed, not fresh per transition ([below](#the-chain)) |
| Every host carrying the topic | the whole record, and every carrier and funding tree | the withheld witness | a host can match an unlinked sub-store root ([below](#sub-stores)) |
| The host you look up at | which identity you asked about, and your network address | who you are | ask several hosts and each learns the same |
| Your header source | the block heights of the proofs you check, and your network address | the identity, the name, the host | |
| The subject's domain | that somebody resolved this name, and from what address | anything after resolution | address by identity key to skip it ([below](#reading)) |
| Anybody watching a BRC-29 payment on the chain | an ordinary output | the payee's name, identity key or token chain | the notice, if intercepted, links them |
| The two parties to a payment | the derivation, and so the link | | |

## The chain

One mined output per transition. The key is a child of your identity key
(BRC-42, BRC-43) and cannot be run backwards. The tag says the output belongs
to this application, not to whom. The hash is the carrier's txid, which
commits to the record without containing it; the record's 32 byte `salt` makes
that commitment hiding, so an observer who guesses the record to the byte still
cannot confirm the guess. The signature shows the output was made by whoever
holds the identity, and nothing else.

**Limit.** The derivation is fixed, and your domain publishes your identity
key. Anyone who knows the key derives the same child, finds every token, and
reads the timing of your updates from block timestamps; each token spends the
previous one, so the sequence is linkable anyway. This is pseudonymity against
a stranger, not concealment from an acquaintance. If *when* you publish is
sensitive, the cadence gives it away.

## What is never written into a block

The record. It lives in a carrier that can never be mined, so a retraction is
real: `bfinger kill` spends the funding outputs the carriers depend on, making
each a double spend, and conforming hosts drop the records.

**Limit.** Retraction removes future availability from honest hosts. It cannot
un-copy what somebody already fetched. The difference is between "no longer
served" and "in every archive of the chain for ever", which is large, and is
not erasure.

## The record

Public to every subscribed host and served without authentication; no
confidentiality is claimed. The one secret is the witness: each record
publishes `SHA-256(w)` and withholds `w`, which appears only inside the next
record and authorizes exactly one next transition.

## Sub-stores

An entry in `refs` is a 32 byte root plus the member `count`, so a store's size
is always public.

- **A linked store is public.** An entry with a `head` lets any reader follow
  it: the one member, or for a larger store a manifest listing every member
  with its size and type.
- **An unlinked store is unlisted, not private.** A leaf is the hash of a
  member's carrier txid, and every subscribed host holds that carrier. A host
  can hash an identity's carriers and match any root in its record. Hiding
  membership from hosts needs a member encoding with a secret under the hash,
  which is not built.

So revealing one member with an inclusion path and nothing about the rest
works only for an unlinked store, against a party that does not hold its
carriers. The arithmetic is built (`bcommon/commit`); the private encoding is a
seam.

## Reading

Nothing in a lookup identifies the reader: no credential, cookie, account,
session or payment. The base answer is free on every conforming host by
specification, and that is a privacy property first: a host that charged for
it would have to identify the payer, and every read would become attributable.

To look someone up without telling their domain, address them by identity key
(`bfinger -host <url> <identity key>`): resolution never happens, and the pin,
keyed by the hex, still catches a changed key. In the other direction,
`-watch` re-resolves on every tick, so a watch left running is a steady signal
to the subject's domain.

## Payments

`bfinger pay` derives a fresh destination per payment from the recipient's
identity key and a random prefix and suffix (BRC-29). No address is exchanged
or reused, and the payment touches no topic, host, lookup route or network
fan-out. The one thing that must travel is the notice (derivation and
outpoint), delivered by any channel; a messagebox (BRC-33) would automate that
and is also where a third party would start seeing who pays whom.

## What stays on your machine

`~/.bfinger/known_keys` lists every address you have looked up, as
`~/.ssh/known_hosts` lists hosts. It is the basis of the check that catches a
substituted key, so it cannot be avoided, and it is a record of your reading.
It is written 0600 in a 0700 directory and never leaves the machine;
`bfinger keys forget <acct>` removes an entry, at the cost of first contact
again.

A publisher's home holds the identity key, the wallet, the journal and
`state.json` with the withheld witness. The witness exists nowhere else, so the
home is the most sensitive thing here and the one thing the network cannot
reconstruct.

## What would change each limit

None of these are built.

| Limit | What would change it |
| --- | --- |
| The record is public to every host | Keyed content and conditional key release (BRC-369). A lookup price cannot: every host holds a copy, so only encryption survives copying. Out of scope ([committed-record.md](committed-record.md) section 14.5) |
| Anyone with your key can trace your tokens | A fresh derivation per transition, at the cost of readers no longer finding your token from your key alone |
| The subject's domain sees resolutions | Addressing by key (works today), or a resolution cache with a deliberate lifetime (does not exist) |
| A payment notice is hand delivered | A messagebox (BRC-33), which adds a party who sees the metadata |
| An unlinked store's membership is matchable by hosts | A member encoding carrying a secret the hash covers |

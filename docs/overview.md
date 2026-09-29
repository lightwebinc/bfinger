# Overview

Why bfinger is built the way it is. The [README](../README.md) is the short
version; [architecture.md](architecture.md) is the model (what talks to
what), [privacy.md](privacy.md) is who learns what, and
[committed-record.md](committed-record.md) is the normative specification.

## The old command, and the one thing that changed

`finger alice@example.com` once asked a program on example.com's machine who
alice was. The answer was true because you trusted that machine; take the
machine away, or take it over, and finger had nothing left to say.

bfinger keeps the question and moves the truth. An answer arrives signed by
the address's own key with a Bitcoin proof attached, and the tool checks both
before it prints a line. `VERIFIED` means those checks ran and passed. Nothing
is believed because of where it came from.

## Somebody else holds the data, and that is fine

A record is not kept on its owner's server and is not written into the
blockchain. Copies sit with overlay hosts that carry records of this kind, and
a lookup asks one of them.

A host that edits a record, drops a field or swaps a key produces something
that fails the check, so the reader refuses it and exits non-zero. No host is
trusted, so hosts are interchangeable: any of them can answer, a reader can
ask several and compare (`-quorum 2`), and an address stays reachable without
its owner running any infrastructure. Availability stops being a question of
trust and becomes one of plumbing.

## The transaction that is never meant to be mined

A record travels inside an ordinary Bitcoin transaction, the carrier, built
so that it can never confirm: zero fee, a lock time of 2100-01-01 and a
non-final input. The wrapper still gives three things for nothing:

- **A name.** The carrier's txid is a 32 byte name for exactly those bytes.
- **Proof of belonging.** The carrier spends an output of a mined parent, so a
  reader follows it back to a block with ordinary SPV.
- **A stop button.** Spend that parent output and every carrier hanging off it
  is a double spend. One transaction retracts a record, or every record a
  funding tree paid for, and no conforming host serves them afterwards.

What does reach a block is a small state token per update, carrying a tag and
the carrier's txid, each token spending the last so the history cannot fork.
Mine the state, not the data: the chain orders events, and the data stays off
it where storage is cheap and retraction is real. A non-final transaction
normally holds something back until it is ready; here the same device holds
it back permanently.

## What it gives away, and what it does not

The chain sees a derived key, a tag, a hash and a signature; never a name, a
domain or a field. Reading is anonymous: a lookup carries no account,
credential or payment, which is also why the base answer is free on every
conforming host by specification (a directory that charged would have to know
who was asking). The record itself is public to every host that carries it,
so write it like a bulletin board entry. [privacy.md](privacy.md) gives each
property with its limit.

## What this demonstrates

**A familiar pattern under a Bitcoin-first security model.** Authority lives
in keys rather than servers, the reader verifies instead of trusting, a claim
is true because its subject signed it, and revocation is a spend rather than a
request. A command with four lines of output shows these practices more
plainly than a payments system.

**BEEF as the unit of distribution.** BEEF carries a transaction with the
proofs its ancestry needs, so a receiver checks it against block headers
alone. The objects a publisher submits are the bytes a reader gets back and
checks. That works at any scale: one self-hosted host with no network behind
it is a complete deployment ([self-host.md](self-host.md)), and a BSV
multicast network can deliver each object to every subscribed host at once,
with no host pulling from another and no origin to fail. Both shapes are
drawn in [architecture.md](architecture.md#deployment-shapes).

## Where payment fits

**Paying someone you looked up** is built:
`bfinger pay alice@example.com 5000 -yes` derives a fresh destination from the identity key the lookup
verified (BRC-29), pays it, waits for the proof, and writes the notice the
recipient claims it with to `payments/<txid>.json`. Delivering the notice is
yours to arrange (the messagebox is a seam). You pay the key that just proved
it controls the name, not an address pasted from an email.

**Your own wallet can do it all.** With `wallet = wire` and
`funding = wallet`, a BRC-100 wallet on your machine holds the identity, funds
and broadcasts every mined transaction, and receives payments; bfinger keeps
only the carrier, which no wallet may mine. `tools/walletd` is one such wallet.

**Publishing costs one mined transaction per update**; the carrier is free
because it is never mined, and no host charges to publish. With an ARC
service as the settlement leg and `proofs = async`, an update is published in
about two seconds with no node of your own, and its proof is collected by the
next owner command. Reading costs nothing.
[architecture.md](architecture.md#what-each-flow-costs) has the figures.

## The seams left open

Documented, not built as products:

- **Charging for answers.** The lookup service sorts questions into classes
  and refuses one carrying a member it does not define, so a price can attach
  to a different question, never to the free base one
  ([architecture.md](architecture.md#host-side-charging-a-seam)).
- **Sub-stores.** A record commits to further stores by Merkle root, fetched
  only when wanted. The store machinery is built (`-store`); named stores such
  as grants, links and media, and delegated writing, are not.
- **Content only a payer can read.** A lookup price sells availability, never
  exclusivity, because every host holds a copy. A real restriction needs
  encryption and conditional key release (BRC-369), which is out of scope.
- **Hosts competing for readers**, where a lookup market (BRC-178) would
  attach to the existing host selection interface
  ([host-selection.md](host-selection.md)).

## Under the hood

One `VERIFIED` line is five checks in order: the domain maps the name to a key
and a host; the host returns the token, the carrier and the previous carrier;
the token's proof is checked against the reader's own header source and the
carrier is proven through its funding parent; the record must be the one the
token commits to, signed under keys derived from its identity; and the pin
must match, the sequence advance, the revealed witness hash to the previous
commitment, and the validity window be open.

The pin is what makes the rest meaningful. A host that could swap the key it
answers with could impersonate anyone it serves, and every signature would
faithfully verify the impostor. A changed key is refused unless a rotation
signed by the pinned key names it. [internals.md](internals.md) maps each
check to its code.

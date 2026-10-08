# bfinger

[![CI](https://github.com/lightwebinc/bfinger/actions/workflows/ci.yml/badge.svg)](https://github.com/lightwebinc/bfinger/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/lightwebinc/bfinger)](https://github.com/lightwebinc/bfinger/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/lightwebinc/bfinger)](go.mod)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

> [!WARNING]
> **Experimental software.** bfinger is part of [bstack](https://github.com/lightwebinc/bstack),
> the applications and patterns built on [BSV Layered Multicast](https://github.com/lightwebinc/bsv-multicast).
> It is published to be built on and improved. Interfaces, formats and behavior may change
> rapidly between releases; pin an exact version.

A Bitcoin-era descendant of the UNIX `finger` command. Ask what
`user@domain` currently says about itself and get an answer that proves
itself, instead of one you trust because of who handed it over.

```console
$ bfinger 1bsv@lightweb.net -yes | head -4
1bsv@lightweb.net
  VERIFIED   signature, sequence 2 (update), proof at height 968974
  key        03dd1a…3902  (pinned 2026-10-07 (first contact))
  org        Lightweb Inc.
$ bfinger 1bsv@lightweb.net -field status
1971 called; the .plan is back.
```

That is a live mainnet address; run it as written.

Publishing your own needs **no node and no server of your own**: install,
`bfinger init`, pay the fund address it prints from your wallet (or hand over
the payment's BEEF), `bfinger fund`, publish. Public services carry the
rest (WhatsOnChain, a public broadcaster), and every one is a setting you can
point at your own node instead.

**New here? Start with [QUICKSTART.md](QUICKSTART.md).**

## How it works

```mermaid
flowchart LR
  R["bfinger (you)"] -->|"1. who is alice?"| D["example.com<br/>manifest + resolve"]
  R -->|"2. lookup"| H["overlay hosts<br/>any replica"]
  R -->|"3. headers"| S["header source<br/>proof of work checked"]
  H -. record + Bitcoin proof .-> R
  O["owner"] -->|publish| H
  O -->|one small tx per update| B[("BSV blockchain")]
  B -.-> S
```

- **The domain names the key.** `example.com` answers which identity key is
  `alice`; your copy of bfinger pins that key on first contact and refuses a
  changed key unless the old one signed the rotation.
- **The domain names its hosts, too.** The same `/manifest.json` lists the
  overlay services that answer for the domain under `metanet.overlays`, as
  [BRC-180](https://github.com/bsv-blockchain/BRCs/blob/master/overlays/0180.md)
  specifies, so bfinger asks the lookup host the domain declares
  (`ls_finger`) and never guesses a hostname. `bfinger domain-docs` writes
  that entry, and `tm_finger` for submissions, for you. A declared host is
  still only a claim: everything it serves is checked.
- **Hosts are replicas, not authorities.** Any host can serve the record.
  One that edits it produces something that fails the check, so bfinger
  refuses it. Ask several with `-quorum 2`.
- **The chain holds the state, not the data.** Each update mines a few
  hundred bytes that commit to the record and spend the previous update, so
  the history cannot fork. The record itself travels beside it in a
  transaction that is never mined, which is why the owner can retract it.
- **You check everything.** Signature, proof, sequence and key, against
  block headers whose proof of work bfinger checks itself. `VERIFIED` means
  all of it passed. Exit status is `0` verified, `1` refused, `2` usage or
  transport failure; never `0` on an unverified answer.

The argument in full: [docs/overview.md](docs/overview.md). bfinger is the
worked example in the pattern paper
[_The bstack: publish once, prove everything, and let users ride free_](https://1bsv.net/papers/bstack-patterns.pdf).

## Install

| | |
| --- | --- |
| Container | `docker run --rm -it ghcr.io/lightwebinc/bfinger alice@example.com` (linux/amd64, linux/arm64) |
| Binary | [Releases](https://github.com/lightwebinc/bfinger/releases): static builds for Linux and macOS, amd64 and arm64, with `SHA256SUMS` |
| Go | `go install github.com/lightwebinc/bfinger/cmd/bfinger@latest` (Go 1.27.1 or later) |
| A host of your own | `HOST=finger.example.com docker compose up -d` with the release's `compose.yaml` ([docs/self-host.md](docs/self-host.md)), or one CloudFormation stack on AWS ([deploy/aws](deploy/aws/README.md)) |

## Documentation

| If you want to | Read |
| --- | --- |
| Try it in five minutes, on mainnet | [QUICKSTART.md](QUICKSTART.md) |
| Read, verify and pin addresses; publish and update your own record | [docs/user-guide.md](docs/user-guide.md) |
| Look up a setting, flag or environment variable | [docs/configuration.md](docs/configuration.md) |
| Copy a working command (mainnet, testnet, a local regtest sandbox) | [docs/examples.md](docs/examples.md) |
| Run a host and make your domain answer for handles | [docs/self-host.md](docs/self-host.md) |
| Load the modules into an overlay host you already run | [host/README.md](host/README.md) |
| Understand why it is built this way | [docs/overview.md](docs/overview.md) |
| See the components and how data and payments flow | [docs/architecture.md](docs/architecture.md) |
| See every operation as a diagram | [docs/flows.md](docs/flows.md) |
| Know who learns what from a lookup or a publication | [docs/privacy.md](docs/privacy.md) |
| Implement a compatible reader or host | [docs/committed-record.md](docs/committed-record.md) (the specification), [docs/known-keys.md](docs/known-keys.md), [docs/host-selection.md](docs/host-selection.md) |
| Find your way around the code | [docs/internals.md](docs/internals.md), [docs/owner-flow.md](docs/owner-flow.md) |

## Status

Built and working end to end: the reader (lookups, `verify`, `-watch`,
`-json`, key pinning), the publisher (`init`, `fund`, `create`, `status`,
`rotate`, `retire`, `kill`, `pay`, `receive`, `publish -resume`, `doctor`,
`domain-docs`, `serve-wallet`), sub-stores, and the two overlay host modules
with a container image and a one-file host stack.

Designed, not built yet: handle-certificate verification (BRC-52, built in
borg, a sibling application; not yet adopted here), a messagebox for payment
notices (bbox, a sibling application; `bfinger pay` writes its notice to a
local file until it delivers over one), delegation and the named stores it
writes, host-side charging for lookups, content only a payer can read
(BRC-369), and a reader-side spend check.
[docs/architecture.md](docs/architecture.md#seams) lists them all, and
[docs/overview.md](docs/overview.md#the-seams-left-open) explains the hook
that makes each possible.

## Build

```bash
make verify         # what CI runs: format, vet, dependency set, licenses, build, tests under -race
make host-docker    # the host modules and their tests, in Node 24
make docker-build   # the command's image
make dist           # the release tarballs
```

Two direct Go dependencies, both pinned exactly and asserted by `make
verify`: [go-sdk](https://github.com/bsv-blockchain/go-sdk) and
[bcommon](https://github.com/lightwebinc/bcommon), the public library that
holds bfinger's generic half.

## License

Apache-2.0. One dependency is under the Open BSV License Version 5, whose
grant is revocable and which limits use to the BSV blockchains; `NOTICE`
explains, and `LICENSE-THIRD-PARTY` carries every license text.

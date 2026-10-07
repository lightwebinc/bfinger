# Worked examples

One example per command and flag, with the output the code prints. What the
checks mean is in [committed-record.md](committed-record.md), the publisher's
files in [owner-flow.md](owner-flow.md), the pin grammar in
[known-keys.md](known-keys.md), which host answers in
[host-selection.md](host-selection.md), and every key, flag and default in
[configuration.md](configuration.md).

## Start here: the real network

These run against BSV mainnet as written. `1bsv@lightweb.net` is a live
address whose domain names its own host, so a reader needs only a header
source. Heights, sequence numbers and dates move as the record is updated.

```console
$ bfinger -header-url woc:main 1bsv@lightweb.net -yes | head -4
1bsv@lightweb.net
  VERIFIED   signature, sequence 2 (update), proof at height 968974
  key        03dd1a…3902  (pinned 2026-10-07 (first contact))
  org        Lightweb Inc.
$ bfinger -header-url woc:main 1bsv@lightweb.net -field status
1971 called; the .plan is back.
$ bfinger -header-url woc:main verify 1bsv@lightweb.net 2>&1 >/dev/null | tail -4   # the trace is on stderr
   ok   pin          matches
   ok   chain        prev 328fe3df65603327e00ca17d871ae5fac75674b7fffb393b64d4089d91abc5c9 seq 1 witness ok
   ok   window       inside
   ok   VERIFIED
$ bfinger -header-url woc:main -json 1bsv@lightweb.net | jq -r '.code, .height'
VERIFIED
968974
$ bfinger keys list
1bsv@lightweb.net secp256k1 03dd1a5075fca0ad20c637c38e1b26153bb7131c9c5e4d15c6fb403bbf454a3902 seq=2 first=... last=... fp=SHA256:b2qjtM8q8o2KNkQBHt7LlZ7Wwle6n8mrwgSaIPJGAgg
```

`-yes` pins the key on first contact without a prompt; leave it off to be
asked. Put `header_url = woc:main` in the config file (or export
`BFINGER_HEADER_URL=woc:main`) to drop the flag.

**Publishing on mainnet** needs no node and no server of your own, only coin
you already own. The chain view defaults to WhatsOnChain (`chain = woc:main`)
and the broadcaster to GorillaPool's public arcade (`settle = arcade:main`). A
config for it:

```
header_url = woc:main
facade     = https://finger.example.com          # a host that carries tm_finger
proofs     = async
```

```console
$ bfinger init                        # prints the identity key and a mainnet fund address
$ # send about 10,000 satoshis from your own wallet to that address
$ bfinger fund -txid <txid your wallet shows>   # once it has a block
$ bfinger fund -beef payment.beef     # or: the BEEF your wallet handed over, mined or not
$ bfinger create alice@example.com -set status="hello, world"           # dry run: builds, prints, sends nothing
$ bfinger create alice@example.com -set status="hello, world" -yes
$ bfinger status "back soon" -yes
$ bfinger publish -resume             # later: collects the proofs async left pending
```

bfinger pays the network's miner fee, 100 satoshis per 1,000 bytes, with a
250 satoshi floor per transaction. An update's token is under 500 bytes (the
live record's latest is 458), so an update pays the floor, and the first
record also mines a funding tree holding one funding output for each of the
next 16 carriers. Ten thousand satoshis covers the first record and many
updates. Example 26 has the output.

**Testnet** works end to end the same way: set `network = test` and
`header_url = woc:test` (the chain view and broadcaster follow: `woc:test`,
`arcade:test`), fund the testnet address
`init` prints from a testnet faucet or wallet, and import it with `fund
-txid` (read from WhatsOnChain's testnet API). No public host carries
`tm_finger` on testnet, so publish to a host you run with `HEADERS=woc:test`
([self-host.md](self-host.md)) and read with `-host` pointing at it.

**A local development sandbox** (a private regtest chain, no coin needed)
is the one place `fund` mines coinbase: example 18.

## Conventions

**Output is exact in shape, illustrative in value.** Columns, spacing and
wording are the code's own; keys, txids and heights are placeholders.

| Placeholder | Stands for |
| --- | --- |
| `alice@example.com`, `bob@example.com` | addresses |
| `0324653eac…0ab1c` | an identity key: the golden test vector's, whose private key (`0x42` repeated) is published in `testdata/golden/finger-v1.json`, so it is nobody's identity |
| `027f31ebc5…a2007`, `032c0b7cf9…80991` | test keys from `0x43` and `0x44` repeated: a rotation's successor, and a key that is not the pinned one |
| `1111…`, `2222…`, `3333…`, `6666…` | token, carrier, previous carrier and funding tree txids |
| `https://finger.example.com` | an overlay host; its `/submit` is the facade |
| `192.0.2.30:443`, `192.0.2.31:443` | the addresses the host name resolves to; traces name the one that answered |

**stdout carries the answer, stderr the working** (traces, prompts, progress),
so `bfinger alice@example.com -field status` is safe in a pipeline. **Exit
codes:** `0` verified or done, `1` refused or not found, `2` usage,
configuration or transport failure.

## The configuration used throughout

The file is `-config`, else `$BFINGER_HOME/config`, else
`$XDG_CONFIG_HOME/bfinger/config`, else `~/.bfinger/config`. The examples use:

```
header_url = woc:main
facade     = https://finger.example.com
settle     = tcp:192.0.2.20:8000
rpc        = http://192.0.2.10:8332
asset      = http://192.0.2.10:8090
```

`header_url`, `host` and `facade` have no default. A reader needs
`header_url` alone: the domain's manifest names the host (traces say
`(manifest)`). Set `host` for a lookup by key or a domain with no `ls_finger`.

### Header sources and `network`

```
header_url = woc:main                                    # public WhatsOnChain
header_url = woc:test                                    # needs network = test
header_url = chaintracks:https://headers.example.com/v2  # a chaintracks v2 service
header_url = https://bridge.example.com                  # a bridge's /v1/tip and /v1/root/{height}
network    = main                                        # main (default), test or regtest
```

WhatsOnChain and chaintracks headers must carry the work their bits claim,
and on `main` at least difficulty 4e9; a bridge serves roots only, so nothing
further is checked. `network` also sets the fund address prefix, and a `woc:`
source must match it. Mistakes exit `2` before anything is contacted (one run
per line):

```console
$ bfinger doctor
bfinger: /home/alice/.bfinger/config: config: unknown key: line 9: "nonsense"
bfinger: /home/alice/.bfinger/config: config line 1: not key = value
bfinger: config: network must be main, test or regtest, got "mainnet"
bfinger: header source woc:test is the test network but network is main; set network = test
bfinger: headers: bad header source: "woc:regtest": WhatsOnChain serves woc:main or woc:test
bfinger: headers: bad header source: "bridge.example.com": not an http or https URL (use woc:main, chaintracks:URL or a bridge URL)
```

## Where flags go

Global flags (`-config`, `-home`, `-host`, `-header-url`, `-known-keys`,
`-quorum`, `-timeout`, `-version`) go before the address or command. Reader
flags work on either side of the address, and before `verify`:

```console
$ bfinger -ansi alice@example.com         # the same as: bfinger alice@example.com -ansi
$ bfinger -l verify alice@example.com     # the same as: bfinger verify alice@example.com -l
$ bfinger alice@example.com -header-url woc:main
flag provided but not defined: -header-url
Usage of lookup:
       ... the reader's flags, and exit 2
```

A reader flag before any other command (`bfinger -l doctor`) is refused the
same way, with the top-level usage. A subcommand's flags may sit on either
side of its positional: `bfinger keys trust alice@example.com -key <hex>`
equals `bfinger keys trust -key <hex> alice@example.com`.

**Help.** `-h`, `-help` and `--help` print the usage and exit `0` on every
command, doing nothing else (`bfinger init -h` creates no identity). Bare
`help` is a help request only at the top level and for `keys` (and its
subcommands), `init`, `receive`, `doctor` and `serve-wallet`; elsewhere it is
an argument (`bfinger status help` sets the status to `help`) or a stray word
refused at exit `2` (`fund help`, `publish help`). `bfinger -h`
and `bfinger help` are answered before the config file is read; a
subcommand's `-h` after, so a config the loader refuses stops it at exit `2`.

---

# Reading

## 1. A lookup, first contact and `-yes`

```console
$ bfinger alice@example.com
first contact with alice@example.com
  identity key 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c
  fingerprint  SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc
  answered by  192.0.2.30:443, sequence 7, mined=true
Trust this key? [y/N] y
alice@example.com
  VERIFIED   signature, sequence 7 (update), proof at height 912430
  key        032465…ab1c  (pinned 2026-09-24 (first contact))
  plan       shipping 2.0
  status     available
```

Later lookups print `(pinned 2026-09-24)`. `-yes` pins without asking, which
a script or container needs. When stdin is not a terminal the run prints `not
a terminal and no -yes given: refusing to pin on first contact` and ends
`REFUSED-KEY first contact not accepted`, exit `1`. `/dev/null` is a character
device, so it gets the prompt, reads nothing, and is refused the same way.

## 2. `-l`, the whole record

```console
$ bfinger alice@example.com -l
alice@example.com
  VERIFIED   signature, sequence 7 (update), proof at height 912430
  key        0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c  (pinned 2026-04-11)
  fp         SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc
  plan       shipping 2.0
  status     available
  bio        Ships things. Answers mail on Tuesdays.
  token      1111111111111111111111111111111111111111111111111111111111111111
  carrier    2222222222222222222222222222222222222222222222222222222222222222
  previous   3333333333333333333333333333333333333333333333333333333333333333
  window     2026-09-10T00:26:40Z to 2026-10-14T17:46:40Z
  store      bio carrier 8888888888888888888888888888888888888888888888888888888888888888
  ref        bio root 4444444444444444444444444444444444444444444444444444444444444444 count 1
  host       192.0.2.30:443
```

`bio` is a sub-store (example 21); a failed store prints its code and reason
instead. `window` appears only when set (`unbounded` for an open end). `fp`
prints even on a refusal, for checking a key change by another channel.

## 3. `-json`

One object, two-space indented, stable schema (example 2's record):

```console
$ bfinger -json alice@example.com
{
  "acct": "alice@example.com",
  "code": "VERIFIED",
  "identityKey": "0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c",
  "fingerprint": "SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc",
  "pinned": "2026-04-11",
  "seq": 7,
  "kind": "update",
  "mined": true,
  "height": 912430,
  "tokenTxid": "1111111111111111111111111111111111111111111111111111111111111111",
  "carrierTxid": "2222222222222222222222222222222222222222222222222222222222222222",
  "prevCarrierTxid": "3333333333333333333333333333333333333333333333333333333333333333",
  "host": "192.0.2.30:443",
  "hostsAgreeing": [
    "192.0.2.30:443"
  ],
  "window": [
    1789000000,
    1792000000
  ],
  "body": {
    "plan": "shipping 2.0",
    "status": "available"
  },
  "refs": [ ... ],
  "stores": [ ... ],
  "steps": [ ... ]
}
```

| Field | Present when | Meaning |
| --- | --- | --- |
| `acct` | always | the address as parsed: lower-cased, `acct:` and a leading `@` stripped, any `+tag` removed; for a lookup by key, the lower-cased hex |
| `code` | always | `VERIFIED`, `VERIFIED-UNMINED`, `RECORD-PENDING`, `NO-TOKEN`, `REFUSED-DECODE`, `REFUSED-KEY-DERIVE`, `REFUSED-SIG`, `REFUSED-KEY`, `REFUSED-SEQ`, `REFUSED-FORK`, `REFUSED-EXPIRED`, `REFUSED-BUMP`, `REFUSED-COMMIT`, `REFUSED-WITNESS`, `REFUSED-MINEABLE`, `REFUSED-RETIRED` |
| `reason` | anything but a pass | the prose behind the code |
| `identityKey`, `fingerprint` | once a key is known | before the record decodes, the key the domain resolved, not one the record proved |
| `pinned` | a pass | the pin's first-contact date, plus ` (first contact)` or ` (rotated today)` when this run wrote it |
| `seq`, `kind` | the record decoded | `kind` is `create`, `update`, `rotate` or `retire` |
| `mined`, `height` | always; `height` when mined | whether the state token carries a proof (`false` is not a refusal, example 8) |
| `tokenTxid`, `carrierTxid`, `prevCarrierTxid` | as far as verification got | the token, its carrier, the carrier `prev` names |
| `successor` | a `rotate` record | the key the identity continues under |
| `host`, `hostsAgreeing` | after a lookup | the first host that answered; every host whose answer matched byte for byte |
| `window` | always | `[notBefore, notAfter]` in unix seconds, `0` unbounded |
| `body` | the record has fields | byte strings as hex; maps and arrays recurse |
| `refs` | the record commits to sub-stores | per store: `count`, `head` when linked, `name`, `root` |
| `stores` | the record links stores | per store as read: `name`, its own `code` (a failed store does not fail the record; `UNSUPPORTED` means this build lacks the feature), `reason`, `count`, `carrierTxid` for one member or `manifestTxid` and `memberTxids`, `body` |
| `steps` | verification ran | the trace of example 6, one `{"Name", "OK", "Detail"}` per step (capitalized, unlike every other field) |

A `REFUSED-FORK` is decided before verification: only `acct`, `code`,
`reason`, `mined` (`false`) and `window` (`[0, 0]`), no `steps`.

## 4. `-field`

```console
$ bfinger alice@example.com -field status
available
```

One body or store field, nothing else. It prints nothing for a missing field
(exit `0`) **and** nothing on a refusal (exit `1`); only the exit code tells
them apart (example 31).

## 5. Reading by identity key

```console
$ bfinger -host https://finger.example.com 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c

$ bfinger 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c
bfinger: looking up an identity by key needs -host (there is no domain to consult)
```

Any 66 hex characters starting `02` or `03` that spell a point in its one
encoding are a key; one off the curve, or with `x` at or above the field
prime, is refused with `not a valid compressed key`. The trace starts `identity given
as a key; no name resolution`. The pin is keyed by the hex, apart from the
address's, and the check that domain and record name one identity is skipped.

## 6. `verify`, `-v` and the trace

`verify` runs a lookup's checks, always prints the trace, and defaults
`-accept-unmined` to false. `-v` gives a lookup the same trace.

```console
$ bfinger verify alice@example.com
   resolved alice@example.com to 0324653eac43 via https://example.com/manifest.json
   host https://finger.example.com (manifest)
   192.0.2.30:443 answered 3 output(s)
   ok   answer       3 output(s)
   ok   decode       token 1111111111111111111111111111111111111111111111111111111111111111
   ok   token-spv    mined=true height=912430
   ok   carrier      2222222222222222222222222222222222222222222222222222222222222222
   ok   signatures   token and carrier verify under the identity's derived keys
   ok   pin          matches
   ok   chain        prev 3333333333333333333333333333333333333333333333333333333333333333 seq 6 witness ok
   ok   window       inside
   ok   VERIFIED     
alice@example.com
  VERIFIED   signature, sequence 7 (update), proof at height 912430
  key        032465…ab1c  (pinned 2026-04-11)
  status     available
```

`pin` says `first contact`, `matches` or `rotation from the pinned key
verified`. A refusal names its step:

```console
$ bfinger verify alice@example.com
       ... the same up to signatures
   FAIL REFUSED-KEY  identity key changed and no rotation signed by the pinned key names it (pinned 0324653eac43)
alice@example.com
  REFUSED-KEY identity key changed and no rotation signed by the pinned key names it (pinned 0324653eac43)
  key        032c0b…0991  (pinned 2026-04-11, unchanged)
```

The reason names the stored key, the `key` line the one answered: the
signatures verified and the key still changed.

## 7. `-quorum`

Asks that many of the addresses the host name resolves to (DNS, see
[host-selection.md](host-selection.md)) and refuses if they differ:

```console
$ bfinger -quorum 2 alice@example.com
alice@example.com
  REFUSED-FORK hosts disagree: 192.0.2.30:443:3 outputs vs 192.0.2.31:443:2 outputs

$ bfinger -quorum 2 -host https://192.0.2.10 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c
bfinger: lookup at https://192.0.2.10: hostset: https://192.0.2.10: quorum 2 exceeds the 1 host(s) the name resolves to
```

A count mismatch is asked again up to four times, three seconds apart, with
`hosts differ in output count; waiting 3s for delivery (attempt 1)` in the
trace: a host a few seconds behind is the delivery window, not a fork. Equal
counts with different bytes are a fork at once.

## 8. `-accept-unmined`

`VERIFIED-UNMINED`: every check passed, the state token verified through its
ancestry and is not yet in a block. A lookup exits `0` on it, `verify` `1`:

```console
$ bfinger alice@example.com
alice@example.com
  VERIFIED-UNMINED signature, sequence 8 (update), unmined
  key        032465…ab1c  (pinned 2026-04-11)
  status     back Monday
$ bfinger alice@example.com -accept-unmined=false    # exit 1, like verify
$ bfinger verify alice@example.com -accept-unmined   # exit 0, like a lookup
```

The negative form needs `=`. An unmined answer does not advance the pinned
sequence. `RECORD-PENDING` differs: the token verified but the host has not
served the carrier holding the record.

## 9. `-watch`, `-interval`, `-once`

Prints each change of verdict, key, sequence or carrier under a timestamp;
every poll is verified and pinned like a single lookup.

```console
$ bfinger alice@example.com -watch -interval 30s -once
2026-09-24T14:02:11Z
alice@example.com
  VERIFIED   signature, sequence 7 (update), proof at height 912430
       ... until the next change, printed the same way
```

`-interval` defaults to `15s`. `-once` exits after the first change with that
answer's status, as a single lookup would; without it the watch runs until
interrupted and then exits `0`. A failed poll prints `watch: <error>` and
polling continues.

## 10. `-ansi` and `-ascii`

Body text is filtered before it reaches the terminal: control characters,
escape sequences, zero-width and bidirectional characters are dropped, a tab
becomes a space, and a value stops at 200 lines of 512 columns with
`[truncated]`. A multi-line value prints under its label, indented.

```console
$ bfinger alice@example.com -field motd -ansi
$ bfinger alice@example.com -field status -ascii
caf? open
```

`-ansi` lets a record's color through (SGR only, re-validated, each colored
value ending with a reset), whatever `NO_COLOR` and `TERM` say. `-ascii`
prints every character above `~` (0x7E) as `?`; it is the default when the
locale is not UTF-8.

## 11. The header source: missing, behind, lying

```console
$ bfinger -config /dev/null alice@example.com
bfinger: no -header-url configured; VERIFIED needs a header source and there is no default (for mainnet: -header-url woc:main)
$ bfinger alice@example.com        # the source is behind the proof's block: exit 1
alice@example.com
  REFUSED-BUMP token proof at height 912430 is not in the header source
$ bfinger alice@example.com        # the source failed or lied: no verdict, exit 2
bfinger: could not verify: token proof could not be checked: headers: header fails its proof of work: height 912430: bits 207fffff claim less work than the network floor
```

The first is checked before any network call; `-header-url ""` does not unset
a configured source, since a flag at its zero value never hides the file. A
source is behind when it answers 404 for the height. Any other status but
200, or a header without the work it claims, is a failure.

---

# Pins

## 12. `keys trust`

Adds a pin out of band, for a key obtained some other way:

```console
$ bfinger keys trust alice@example.com -key 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c
pinned alice@example.com 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc
```

The address is normalized, so `BOB+work@Example.COM` pins `bob@example.com`.
Refusals (the first three exit `1`, the rest `2`):

```
bfinger: fingerprint mismatch: key is SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc, wanted SHA256:notthisone
bfinger: alice@example.com is already pinned; -force replaces it, `keys forget` removes it
bfinger: carol@example.com is retired; trusting it again is not something to do by accident; -force replaces it, `keys forget` removes it
bfinger: keys trust: -key must be a 33-byte compressed key in hex
bfinger: keys trust: -key is required; run `bfinger alice@example.com` to pin on first contact instead
```

`-fingerprint SHA256:…` refuses a key that does not match it. The `02`/`03`
prefix is checked as well as the length, because half a pasted uncompressed
key fingerprints as cleanly as a real one, and so is the point: a key off the
curve, or a second spelling of one on it, is refused the same way. The pin is
written in lower-case hex whatever case was typed. A retired address counts as
pinned; `-force` removes the retirement record and pins in its place. Use it
when the owner has come back with a new key by a channel you trust, not to
get past an unexplained refusal.

## 13. `keys list` and `keys forget`

```console
$ bfinger keys list
alice@example.com secp256k1 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c first=2026-04-11T09:00:00Z last=2026-09-24T14:02:11Z fp=SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc
$ bfinger keys forget alice@example.com
forgot alice@example.com (1 line(s) removed)
$ bfinger keys forget alice@example.com
bfinger: no pin for alice@example.com
```

An empty store lists `no pins in <path>` on stderr, exit `0`. `forget` keeps
the `@rotated-from` history unless `-all`, and exits `1` if nothing matched.

## 14. `keys verify`

Re-reads every active pin against the hosts. Writes nothing.

```console
$ bfinger keys verify
alice@example.com                        VERIFIED seq 7 unchanged
bob@example.com                          VERIFIED seq 12 advanced to seq 12
dana@example.com                         VERIFIED seq 4 ROTATED to 027f31ebc546 (run a lookup to re-pin)
dave@example.com                         VERIFIED seq 9 RETIRED
erin@example.com                         REFUSED-KEY the record names a different identity than the domain resolved
frank@example.com                        UNREACHABLE manifest for example.com: resolve: manifest for example.com: status 404
```

`advanced` means published since the pin was written: not drift, a lookup
moves the pin. `ROTATED` is a verified rotation from the pinned key, applied
by a plain lookup, never here. `RETIRED`: a later lookup marks the pin
`@retired`. A refusal code is the line to act on. `UNREACHABLE` carries the
transport error verbatim. Exit `1` on any refusal, `UNREACHABLE`, `ROTATED` or
`RETIRED`, else `0` (no pins included). The address column is padded to 40
characters and the code is one word, so `while read -r address code rest`
splits a line; capture the output before looping, because a pipeline loses
the exit code.

## 15. When the pin store will not load

The run is refused rather than continuing without a pin, which would revert
an address to first-contact trust. Exit `2`, one run per line:

```console
$ bfinger keys list
bfinger: known_keys: refusing a file that is group or world writable: /home/alice/.bfinger/known_keys is mode 0664
bfinger: known_keys line 2: unknown field "bogus"; this binary does not understand it and will not guess
bfinger: known_keys line 2: algo "ed25519" is not secp256k1
bfinger: known_keys line 3: alice@example.com already has an active pin at line 2; at most one per address
```

The fix for the first is `chmod 600`. `doctor` prints the message on its
`known_keys` line and still exits `0`. A lookup loads the store after the
manifest and host answer, so an unreachable domain fails on the domain first.

---

# Publishing

## 16. `init`

Creates the identity key and an empty wallet; contacts nothing, needs no
configuration.

```console
$ bfinger init
created /home/alice/.bfinger
identity key 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c
fingerprint  SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc
fund address 113JUEFbhsMD9GiuTFTXV1Vrz9a9MrmC1v
$ ls -l ~/.bfinger
-rw------- 1 alice alice  62 Sep 24 14:02 identity.json
-rw------- 1 alice alice  21 Sep 24 14:02 wallet.json
```

The directory is `0700`. Run again, it prints `identity already exists in
/home/alice/.bfinger` on stderr and the same three lines. With `network =
test` or `regtest` the fund address has the testnet prefix
(`mfZFmHLaWtnTvPCXApRuJviBr9ArFNxfhE` for this key). With `wallet = wire` the
key stays in the wallet and the first line is `created <home> for the wire
wallet at <wallet_url>`.

## 17. `fund -txid` and `fund -beef`: import a payment you sent

The way to fund a wallet on mainnet or testnet: send a small amount from your
own wallet to the fund address `init` printed, wait for one block, and import
the transaction by its txid:

```console
$ bfinger fund -txid 9999999999999999999999999999999999999999999999999999999999999999
imported 1 output(s), 50000 sat, mined at height 912400; wallet 1 output(s), 50000 sat
$ bfinger fund -txid 9999999999999999999999999999999999999999999999999999999999999999
9999999999999999999999999999999999999999999999999999999999999999 already imported; wallet 1 output(s), 50000 sat
```

Every output paying the fund address is added with its transaction and proof.
The transaction comes from the chain view (`chain`: WhatsOnChain for
`network` by default, a node with `asset:<url>`) and is not trusted: its txid
must match and its proof must check against `header_url`. Refusals:

```
bfinger: not a transaction id: 9999
bfinger: needs header_url: the proof is checked against it
bfinger: no chain view on network regtest: configure chain = asset:<url> (a node's asset API) to import from it
bfinger: 9999…9999: the chain view has no such transaction; check the txid and the network
bfinger: 9999…9999: not mined yet; import it once it has a block, or hand over the wallet's BEEF with fund -beef
bfinger: 9999…9999: its proof is not in the header source
bfinger: 9999…9999 pays nothing to this home's fund address 113JUEFbhsMD9GiuTFTXV1Vrz9a9MrmC1v
```

A wallet that hands over the payment as BEEF (binary or hex, from a file or
`-` for standard input) needs no lookup and no wait for the block:

```console
$ bfinger fund -beef payment.beef
imported 1 output(s), 50000 sat, mined at height 912400; wallet 1 output(s), 50000 sat
$ bfinger fund -beef unmined.beef
imported 1 output(s), 50000 sat, not mined yet: held until its proof is collected by a later command; wallet 2 output(s), 100000 sat
$ bfinger fund -beef unmined.beef -unmined refuse
bfinger: the payment: not mined yet; import it once it has a block, or hand over the wallet's BEEF with fund -beef
```

A mined payment's proof is checked against `header_url`. An unmined one is
taken (unless `-unmined refuse` or `fund_unmined = refuse`) when it can mine
as it stands, every transaction it spends carries a proof `header_url` holds,
and its scripts verify against them; its coin is spendable once a later
command collects its proof from the chain view.

## 18. `fund` without `-txid`: mine coinbase (regtest only)

Coinbase: only on a regtest chain you run (development and tests). It mines
blocks paying the fund key on the configured node (`generatetoaddress`), which
no public network answers; on mainnet and testnet fund the wallet with
`fund -txid` or `fund -beef` (example 17).

```console
$ bfinger fund
wallet before: 0 output(s), 0 sat; node tip 101
wallet after:  101 output(s), 505000000000 sat (+101); immature 100; node tip 202
$ bfinger fund -rescan -blocks 200
wallet before: 45 output(s), 225000000000 sat; node tip 302
wallet after:  101 output(s), 505000000000 sat (+56); immature 100; node tip 302
```

`-blocks 101` (default) matures the first coinbase; `-batch 30` is blocks per
call. `-rescan` re-reads the last `-blocks` blocks for coinbase this wallet
holds, after a run that stopped between mining and recording. The wallet is
opened before the node is looked for, so a home with no identity reports
``open wallet in /home/alice/.bfinger: bwallet: open identity: ... (run
`bfinger init`)``, and one with no node `rpc and asset must be configured for
coinbase funding (config keys rpc, asset), which is only for a regtest chain
you run`.

## 19. `domain-docs`

Writes what a domain serves so readers can find a handle. Contacts nothing.

```console
$ bfinger domain-docs alice@example.com -host https://finger.example.com
wrote bfinger-site/manifest.json
wrote bfinger-site/.well-known/metanet-handles/by-handle/alice.json

1. Serve bfinger-site/manifest.json at https://example.com/manifest.json (same host, no redirect to another host).
2. Point DNS for finger.example.com at the host, and give the host stack this directory (it serves the resolve endpoint from it).
3. Check: bfinger alice@example.com -v
```

`alice.json` is the resolve answer, the JSON `rotate` prints (example 22).
`manifest.json` names the host as both `ls_finger` and the configured `topic`,
the resolve endpoint `https://finger.example.com/.well-known/metanet-handles/resolve`,
and, if it has none, a `trust` entry with the first handle's key. The key is
this home's identity unless `-key <66 hex>` is given; `-out` (default
`bfinger-site`) is the directory. Run once per handle into the same directory:
the manifest keeps its other fields and handles. Refusals, exit `2`:

```
bfinger: domain-docs: exactly one address (handle@domain) is required
bfinger: domain-docs: "Alice_@example.com" is not handle@domain (BRC-169 section 2.1)
bfinger: domain-docs: -host must be the host's https base URL, for example https://finger.example.com
bfinger: domain-docs: -key must be a compressed public key, 66 hex characters starting 02 or 03
```

A handle is 1 to 64 of `a-z 0-9 . _ -`, starting and ending with a letter or
digit. `-host` must be `https` with no path. [self-host.md](self-host.md)
serves the result.

## 20. `create`, and running without `-yes`

```console
$ bfinger create alice@example.com -set status=available -set plan="shipping 2.0" -yes
body 36 of 16384 bytes
funding tree 6666666666666666666666666666666666666666666666666666666666666666: 16 output(s) of 1 sat
funding tree 6666666666666666666666666666666666666666666666666666666666666666: settling via tcp:192.0.2.20:8000 (1180 bytes)
funding tree 6666666666666666666666666666666666666666666666666666666666666666: mined at height 912429
funding tree published: admitted 16 output(s)
record seq 1 kind create: carrier 2222222222222222222222222222222222222222222222222222222222222222 (268 bytes), token 1111111111111111111111111111111111111111111111111111111111111111 (412 bytes)
token 1111111111111111111111111111111111111111111111111111111111111111: settling via tcp:192.0.2.20:8000 (412 bytes)
token 1111111111111111111111111111111111111111111111111111111111111111: mined at height 912430
carrier: admitted output(s) [0] via https://finger.example.com
token: admitted output(s) [0] via https://finger.example.com
alice@example.com seq 1 create
  token   1111111111111111111111111111111111111111111111111111111111111111 (height 912430)
  carrier 2222222222222222222222222222222222222222222222222222222222222222
```

The last three lines are stdout. The funding tree is published first because
the carrier spends one of its outputs; spending that output again makes the
carrier a double spend, which is how `kill` (example 24) reaches every host
that admitted the tree. `-funding-count` (16) and `-funding-sats` (1) size a
new tree. Publishing twice is refused, exit `1`: ``alice@example.com already
published seq 1; use `bfinger status` to update``.

Without `-yes`, or with `-dry-run`, a sending command builds, prints and
sends nothing:

```console
$ bfinger create alice@example.com -set status=available
no -yes given: building only, sending nothing (every owner command spends real funds)
       ... progress, then on stdout: carrier <txid>, its hex, token <txid>, its hex
```

Under `funding = wallet` there is no dry run: every sending command needs
`-yes` and refuses `-dry-run`, exit `2` (`funding = wallet: pass -yes and no
-dry-run; the wallet broadcasts what it signs, so there is no dry run`).

## 21. `status`: `-set`, `-unset`, `-expires`, `-store`, `-unstore`

```console
$ bfinger status "back Monday" -yes
       ... progress as in example 20
alice@example.com seq 8 update
  token   1111111111111111111111111111111111111111111111111111111111111111 (height 912451)
  carrier 2222222222222222222222222222222222222222222222222222222222222222
$ bfinger status -set plan="shipping 2.1" -set room=203 -unset status -yes
$ bfinger status -set plan=@plan.txt -yes         # a value read from a file
$ bfinger status "on leave" -expires 720h -yes   # notAfter 30 days out; 0 is unbounded
$ bfinger status -store bio=@bio.txt -yes        # a sub-store in its own carrier
store "bio": carrier 8888888888888888888888888888888888888888888888888888888888888888 (402 bytes), funding output 1
       ... the record's lines
$ bfinger status -unstore bio -yes               # drop it from the record
```

A bare positional sets `status`. The flags repeat and edit the previous body
rather than replace it. Past `notAfter` readers refuse with
`REFUSED-EXPIRED`, so `-expires` is a promise to publish again in time. A
store name is 1 to 64 bytes; a store too large for one sub-record becomes
parts plus a manifest (`store "bio": 40000 bytes in 3 parts plus a
manifest`), planned before anything is minted. Stores not named carry
forward. Refusals:

```
bfinger: nothing published yet; use `bfinger create`
bfinger: this identity is retired
bfinger: "bob@example.com" looks like an address, not a status. This home publishes as alice@example.com; to publish as that identity, run this with the -config that names its home. To set that text as the status anyway: -set status=bob@example.com
```

## 22. `rotate`, and the transition that completes it

```console
$ bfinger rotate -yes
       ... progress
alice@example.com seq 9 rotate
  token   1111111111111111111111111111111111111111111111111111111111111111 (height 912470)
  carrier 2222222222222222222222222222222222222222222222222222222222222222
rotation published; successor 027f31ebc5462c1fdce1b737ecff52d37d75dea43ce11c74d25aa297165faa2007
next: publish one transition under the new key (`bfinger status ... -yes`), then update the domain's resolve document:
  {"metanetHandles":"1.0","handle":"alice","domain":"example.com","identityKey":"027f31ebc5462c1fdce1b737ecff52d37d75dea43ce11c74d25aa297165faa2007","ttl":3600,"revoked":false}
```

Signed by the current key, it names a successor: a fresh key at
`~/.bfinger/successor/identity.json`, or `-successor <66 hex>`, a key this
home can sign with (in wire mode the wallet's identity by default). Readers
now see `successor  rotated to 027f31…` under the carried-over body. The next
transition signs under the new key, and mints a new funding tree because the
new key cannot spend the old one:

```console
$ bfinger status "under the new key" -yes
funding tree 6666666666666666666666666666666666666666666666666666666666666666: 16 output(s) of 1 sat
       ... progress
rotation complete: identity.json is now the primary; the previous key is kept as identity-prev-9.json
       ... the result lines
$ bfinger alice@example.com
       ...
  key        027f31…2007  (pinned 2026-09-24 (rotated today))
```

The old key file is kept, since the wallet holds coin locked to it. Then
serve the new resolve answer (`domain-docs -key <new key>` writes it): until
the domain names the new key, readers refuse with `REFUSED-KEY`. If someone
else runs the domain, agree this step beforehand. A second `rotate` first is
refused: ``a rotation to 027f31ebc546 is already pending; publish one
transition (`bfinger status`) to complete it``.

## 23. `retire`

Publishes the terminal record, with an empty body; the token is never spent
again.

```console
$ bfinger retire -confirm RETIRE -yes
       ... progress, the three result lines
alice@example.com retired at sequence 11
$ bfinger retire -confirm retire
bfinger: retire is terminal; pass -confirm RETIRE
```

The confirmation is case sensitive, exit `2` without it. Readers mark the pin
`@retired` and refuse later answers; the record stays readable. Refused, exit
`1`: `nothing published yet`, `already retired`, `a rotation is pending;
complete it first`.

## 24. `kill`

Sweeps every funding tree this identity minted, every output, so each
carrier becomes a double spend and hosts that admitted the trees drop the
records.

```console
$ bfinger kill -confirm KILL -yes
sweep 7777777777777777777777777777777777777777777777777777777777777777: 16 funding output(s) of tree 6666666666666666666666666666666666666666666666666666666666666666 (2088 bytes)
sweep 7777777777777777777777777777777777777777777777777777777777777777: sent via tcp:192.0.2.20:8000 (2088 bytes); waiting for its block
sweep 7777777777777777777777777777777777777777777777777777777777777777: mined at height 912540
swept tree 6666666666666666666666666666666666666666666666666666666666666666 with 7777777777777777777777777777777777777777777777777777777777777777 (height 912540); hosts answered admitted=[] duplicate=false
alice@example.com: every record retracted on chain; hosts that saw the sweeps answer nothing
```

`admitted=[]` is expected: a sweep is accepted for its inputs, counted as
`finger_admitted_total{kind="spend"}`. Readers then get `NO-TOKEN` from every
host that admitted the trees; one that never did keeps answering. Hosts
re-read funding outpoints on start, so a kill survives a restart. `retire`
says "stopped"; `kill` retracts every sequence at once. Without `-yes` each
sweep's hex is printed and nothing sent. Without `-confirm KILL`, exit `2`;
with nothing to sweep, exit `1`: `no funding trees recorded; nothing to
sweep`.

## 25. `publish -resume`

Re-sends the objects of the current state, rebuilt from the state file: the
recovery when the token mined but the objects did not land, and the way to
seed a second host.

```console
$ bfinger publish -resume
carrier 2222222222222222222222222222222222222222222222222222222222222222: DUPLICATE at https://finger.example.com (already held)
token 1111111111111111111111111111111111111111111111111111111111111111: admitted [0] at https://finger.example.com
$ bfinger publish -resume -facade https://finger2.example.com
```

It never re-mints or touches the settlement leg, and `DUPLICATE` is not a
failure, so it is safe to repeat. What it sends never depends on the
journal. It first collects proofs that have arrived (example 26). Refusals,
in order: `publish -resume [-facade URL]`, `no facade configured` (exit `2`),
`nothing published yet`, `state has no funding tree; the carrier's parent
cannot be rebuilt` (exit `1`).

## 26. Publishing with no node, and `proofs = async`

The public arcade settles and reports proofs (the default `settle`),
WhatsOnChain answers for anything it does not know (the default `chain`), and
the header source gives the tip, so this needs no node (`rpc`, `asset`). Here
the leg is another arcade installation, named explicitly:

```
header_url = woc:main
facade     = https://finger.example.com
settle     = arcade:https://arc.example.com/v1
proofs     = async
```

`arcade_key` is a bearer token, sent only to that URL. Fund with `fund -txid`
or `fund -beef` (example 17). A transition returns once the network accepts it; against
example 20 only these lines change:

```console
$ bfinger create alice@example.com -set status=available -yes
       ...
funding tree 6666666666666666666666666666666666666666666666666666666666666666: broadcasting via arcade:https://arc.example.com/v1 (1180 bytes)
funding tree 6666666666666666666666666666666666666666666666666666666666666666: accepted; its proof is collected later
       ...
token 1111111111111111111111111111111111111111111111111111111111111111: broadcasting via arcade:https://arc.example.com/v1 (412 bytes)
token 1111111111111111111111111111111111111111111111111111111111111111: accepted; its proof is collected later
       ...
alice@example.com seq 1 create
  token   1111111111111111111111111111111111111111111111111111111111111111 (accepted, proof pending)
  carrier 2222222222222222222222222222222222222222222222222222222222222222
```

Readers see `VERIFIED-UNMINED` until the block. The next transition sent with
`-yes`, or `publish -resume`, collects the proofs, republishes each proven object so
every host upgrades, and releases change held until its parent mined:

```console
$ bfinger publish -resume
token 1111111111111111111111111111111111111111111111111111111111111111: mined at height 912431
token 1111111111111111111111111111111111111111111111111111111111111111: proof published to the hosts
       ... the same two lines for the funding tree
change from 1111111111111111111111111111111111111111111111111111111111111111: mined at height 912431, 1 coin(s) spendable again
       ... then the lines of example 25
```

Not yet mined notes `accepted, proof pending`. A transaction the service
refused prints a `WARNING:` naming what hosts hold that will never mine;
publish a new transition to supersede it. `pay` and `kill` still wait for
their block, polling the arcade installation and then the chain view, after the notice is written or the
sweep posted; a wait that runs out reports the proof as pending and holds the
change until a later command collects its proof. Configuration refusals, exit `2`:

```
bfinger: needs header_url: the chain tip comes from the header source, and every proof is checked against it
bfinger: proofs = async needs a settlement leg that answers: settle = arcade:<url> or rpc:<url>, or funding = wallet. The bare EF ingress acknowledges nothing, so without waiting for the proof there is no evidence the transaction was accepted
bfinger: settle must be arcade:main, arcade:test, arcade:<url> (an arcade installation), arc:<url> (an ARC installation), rpc:<url> (node with acknowledgement) or tcp:<host:port> (bare EF to the ingress): ...
```

On a regtest chain, which has no public broadcaster or chain view:

```
bfinger: settle must be configured on network regtest: arcade:<url>, arc:<url>, rpc:<url> or tcp:<host:port>
bfinger: settle = tcp:192.0.2.20:8000 needs a chain view to read proofs from: set chain = asset:<url> (a node's asset API)
bfinger: with no chain view (chain, or a node's asset) proofs must be async: the proof is collected from the arcade installation by a later command
bfinger: funding = wallet needs a chain view (chain, or a node's asset): the wallet broadcasts through its own service, so only the chain can say when it mined
```

## 27. `doctor`

Local state and what the configured endpoints answer. Reads only, needs no
configuration, exits `0` once it runs.

```console
$ bfinger doctor
home        /home/alice/.bfinger
identity    0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc
wallet      56 output(s), 280000000000 sat
state       alice@example.com seq 8 update token 1111111111111111111111111111111111111111111111111111111111111111 carrier 2222222222222222222222222222222222222222222222222222222222222222
funding     6666666666666666666666666666666666666666666666666666666666666666 9 of 16 output(s) left
known_keys  3 line(s) in /home/alice/.bfinger/known_keys
headers     woc:main tip 912451
node        http://192.0.2.10:8090 tip 912451
chain       asset:http://192.0.2.10:8090
facade      https://finger.example.com
settle      tcp:192.0.2.20:8000, proofs wait
fees        100/1000 sat/bytes, floor 250 (static)
journal     seq 8 update 1111111111111111111111111111111111111111111111111111111111111111 ok
```

Other lines, when they apply:

| Line | When |
| --- | --- |
| `state       never published`, `headers     NOT CONFIGURED (no VERIFIED possible)` | a fresh home; no `header_url` |
| `wallet      absent (<error>)` | no identity in the home |
| `wire wallet <url>` | `wallet = wire` |
| `rotation    PENDING to <key>; publish one transition to complete it` | a rotation awaits completion |
| `proof       token <txid> accepted, pending` (also `funding tree <txid>`, `change from N transaction(s) held until mined`) | `proofs = async`, before the block |
| `basket      <12 hex> ...` | `funding = wallet`: whether the wallet holds each tree's outputs; `MISMATCH` means something else took some, and a transition refuses to spend those |
| `arcade      <url> answering, proofs async` | an `arcade:` or `arc:` leg (the default), instead of `settle` |
| `chain       NONE (no node on network regtest; set chain or asset)` | a regtest chain with no node configured |
| `fees        <rate> sat/bytes, floor <n> (live policy: arc\|cache\|static, <error>)` | `fee_source = arc`: whether the broadcaster's policy answered, a cached answer is in use, or the static rate |
| `<label>     <url>: <error>` | an endpoint did not answer (exit still `0`) |

Each transition writes a `journal` entry before either leg is sent: `ok`,
`not published`, `published, proof pending`, `EF FAILED: <error>` or `PUBLISH
FAILED: <error>`. The last two are what `publish -resume` is for. A journal
that does not parse prints `journal     UNREADABLE: <error>`.

---

# Payments

## 28. `pay`

Resolves the recipient, derives a one-off destination from their identity key
(BRC-29), pays it, waits for the block, and writes a notice.

```console
$ bfinger pay alice@example.com 1000 -yes
payment 5555555555555555555555555555555555555555555555555555555555555555: 1000 sat to alice@example.com (0324653eac43), derivation "Zy9uK3JhbmQ=" "c3VmZml4MDE="
payment 5555555555555555555555555555555555555555555555555555555555555555: sent via tcp:192.0.2.20:8000 (226 bytes); waiting for its block
payment 5555555555555555555555555555555555555555555555555555555555555555: mined at height 912431
paid 1000 sat to alice@example.com
  txid   5555555555555555555555555555555555555555555555555555555555555555 (height 912431)
  notice /home/alice/.bfinger/payments/5555555555555555555555555555555555555555555555555555555555555555.json
```

The recipient may be a bare identity key. The derivation tokens are fresh
and random; the payment is unicast and never touches the directory
([privacy.md](privacy.md)). Without `-yes`, or with `-dry-run`, the
transaction hex is printed and nothing sent. The amount is checked first, so
reversed arguments fail the same way (exit `2`): `pay: satoshis must be a
positive integer`. The recipient is resolved before the node is checked.

The notice, `payments/<txid>.json` (mode `0600`), goes to the recipient by
whatever channel the two parties have (a BRC-33 messagebox would carry it;
none is built):

```json
{
  "protocol": "brc29",
  "senderIdentityKey": "027f31ebc5462c1fdce1b737ecff52d37d75dea43ce11c74d25aa297165faa2007",
  "recipientIdentityKey": "0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c",
  "derivationPrefix": "Zy9uK3JhbmQ=",
  "derivationSuffix": "c3VmZml4MDE=",
  "txid": "5555555555555555555555555555555555555555555555555555555555555555",
  "vout": 0,
  "satoshis": 1000,
  "beef": "0100beef01fe...",
  "height": 912431,
  "sentAt": "2026-09-24T14:12:03.418927Z"
}
```

`beef` carries the payment's proof, so the recipient verifies it against
their own headers without asking anyone.

## 29. `receive`

Verifies the payment against this home's `header_url`, re-derives the key,
checks the output is locked to it, and adds it to the wallet. Spends
nothing, contacts no node.

```console
$ bfinger receive 5555555555555555555555555555555555555555555555555555555555555555.json
received 1000 sat from 027f31ebc546
  outpoint 5555555555555555555555555555555555555555555555555555555555555555:0 (height 912431), verified, now spendable from this wallet
$ bfinger receive 5555555555555555555555555555555555555555555555555555555555555555.json
already held: 5555555555555555555555555555555555555555555555555555555555555555:0
```

Refusals, exit `1`, in the order checked:

| Message | Means |
| --- | --- |
| `notice is not a brc29 payment` | the `protocol` field is something else |
| `notice is addressed to <12 hex>, this wallet is <12 hex>` | another identity's notice |
| `notice txid or output does not match its BEEF` | JSON and transaction disagree |
| `the payment's proof is not in the header source` | also a header source still catching up |
| `the payment does not verify` | the transaction failed verification |
| `output is not locked to the key this wallet derives from the sender; not ours` | usually a wrong prefix or suffix |

No `header_url` is exit `2`: `no -header-url configured; a payment is
verified against headers this wallet received itself`. `receive` takes no
flags; `receive -h` prints its usage.

## 30. `serve-wallet`

Serves this home's embedded wallet over the BRC-100 wallet wire, on loopback
only, for a home with `wallet = wire`. Runs until interrupted.

```console
$ bfinger serve-wallet
serving wallet 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c on http://127.0.0.1:3301
$ bfinger serve-wallet -listen 192.0.2.1:3301
bfinger: serve-wallet: -listen must be a loopback address
```

---

# Scripting and containers

## 31. Read a field and branch on the exit code

```sh
#!/bin/sh
set -eu
rc=0
status=$(bfinger alice@example.com -yes -field status) || rc=$?
case "$rc" in
  0) if [ -z "$status" ]; then echo "verified, no status field" >&2
     else printf 'status: %s\n' "$status"; fi ;;
  1) echo "refused or not found; 'bfinger verify alice@example.com' says which" >&2; exit 1 ;;
  *) echo "could not ask: usage error or transport failure" >&2; exit 2 ;;
esac
```

`|| rc=$?` keeps `set -e` from killing the script on a refusal; `rc=0` gives
`set -u` a value; `"$status"` is quoted because a field is arbitrary text;
`-yes` because a script cannot answer the first-contact prompt.

## 32. Containers

`ghcr.io/lightwebinc/bfinger` (`linux/amd64`, `linux/arm64`) has `bfinger` as
its entrypoint and its state in `/home/nonroot/.bfinger`:

```console
$ docker run --rm -v bfinger:/home/nonroot/.bfinger -e BFINGER_HEADER_URL=woc:main \
    ghcr.io/lightwebinc/bfinger alice@example.com -yes -field status
available
$ docker run --rm --network none ghcr.io/lightwebinc/bfinger -version
bfinger <tag>
```

The volume keeps the pin store between runs. `-yes` because a container
without `-it` has no terminal. The exit status is the command's own, so
example 31 works with `docker run` in place of `bfinger`. A host directory or
another mount path needs [the user guide's steps](user-guide.md#running-in-a-container),
because the command runs as uid 65532.

**Running a host.** `ghcr.io/lightwebinc/finger-host` is the reference
overlay host with `tm_finger` and `ls_finger` built in (same platforms).
[self-host.md](self-host.md) runs it with MySQL and Caddy (TLS and handle
resolution) from one compose file, `HOST=finger.example.com docker compose up
-d`. A host needs no multicast network: on its own it is a complete
deployment, its `/submit` is the facade (example 26), and it catches up from
another host with GASP. Loading the modules into a host run another way is in
[host/README.md](../host/README.md#deploying).

---

# Reference

"Spends" means the command can move coin out of the wallet; each such command
sends nothing without `-yes`. Global flags and every configuration key are in
[configuration.md](configuration.md).

| Command | Flags (example) | Spends |
| --- | --- | --- |
| `bfinger <acct\|key>` | `-l` (2), `-json` (3), `-field` (4), `-v` (6), `-yes` (1), `-accept-unmined` (8), `-watch -interval -once` (9), `-ansi -ascii` (10) | no; writes the pin store |
| `bfinger verify <acct>` | the same, except `-watch` | no |
| `bfinger keys list \| forget <acct>` | `forget -all` (13) | no |
| `bfinger keys trust <acct>` | `-key` required, `-fingerprint`, `-force` (12) | no |
| `bfinger keys verify` | (14) | no |
| `bfinger init` | (16) | no |
| `bfinger fund` | `-txid` (17); coinbase on a regtest chain you run: `-blocks 101`, `-batch 30`, `-rescan` (18) | no |
| `bfinger domain-docs <handle@domain>` | `-host` required, `-key`, `-out bfinger-site` (19) | no |
| `bfinger create <acct>`, `status [<text>]` | `-set`, `-unset`, `-store`, `-unstore`, `-expires`, `-funding-count 16`, `-funding-sats 1`, `-yes`, `-dry-run` (20, 21) | yes |
| `bfinger rotate` | the same, and `-successor` (22) | yes |
| `bfinger retire` | `-confirm RETIRE`, `-yes`, `-dry-run` (23) | yes |
| `bfinger kill` | `-confirm KILL`, `-yes` (24) | yes |
| `bfinger publish -resume` | `-facade` (25) | no |
| `bfinger doctor` | (27) | no |
| `bfinger pay <acct\|key> <sats>` | `-yes`, `-dry-run` (28) | yes |
| `bfinger receive <notice.json>` | none (29) | no |
| `bfinger serve-wallet` | `-listen 127.0.0.1:3301` (30) | no |
| `bfinger version` | also `-version` | no |

## Not built

- **BRC-52 handle certificate verification**: `resolve.VerifyHandleCertificate`
  exists and nothing calls it.
- **A messagebox for payment notices** (BRC-33).
- **Delegation**, **hooks**, and **host-side charging**.
- **A reader-side spend check**: SPV shows an output existed, never that it is
  still unspent.
- **Watching the fund address.** Coin enters the wallet through `fund
  -txid` (a mined payment), `receive`, change, and on a regtest chain you
  run, coinbase from `fund`; a payment
  to the fund address is picked up only when its txid is given to `fund
  -txid`. Nothing imports a key.

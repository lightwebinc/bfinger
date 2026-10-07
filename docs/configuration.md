# Configuration

Every setting, flag and environment variable bfinger reads. A setting is taken
from the first of:

1. a command-line flag, when one exists and was given a non-zero value;
2. the environment, as `BFINGER_<KEY>` with the key in upper case;
3. the config file;
4. the built-in default.

An empty environment variable counts as unset, and a flag left at its zero
value never hides a configured one. Six of the twenty keys have a global flag;
the rest are set only in the file or the environment, which keeps credentials
such as `rpc_pass` and `arcade_key` off command lines.

`header_url`, `host` and `facade` have **no default**. Each is the address of
somebody's deployment, and `header_url` is the egress control: a default would
send verification questions to a server nobody chose. A reader needs only
`header_url`; on mainnet the shortest working value is `woc:main`.

## The config file

The first of these that is set names the file, whether or not it exists:

| Order | Path |
| --- | --- |
| 1 | `-config PATH` |
| 2 | `$BFINGER_HOME/config` |
| 3 | `$XDG_CONFIG_HOME/bfinger/config` |
| 4 | `~/.bfinger/config` |

A missing file means the defaults. The grammar is one `key = value` per line;
a line starting `#` is a comment and blank lines are ignored. There are no
sections, quoting, continuations or inline comments, so
`host = https://a.example # note` stores the note too.

```
# ~/.bfinger/config
header_url = woc:main
timeout    = 15s
quorum     = 2
```

An unknown key stops every command, `doctor` included, at exit 2, because an
ignored misspelling would leave a setting at its default with no sign:

```console
$ bfinger doctor
bfinger: /home/alice/.bfinger/config: config: unknown key: line 1: "hots"
```

- **`-home` does not move the config file**, which is read before flags are
  applied: `bfinger -home /tmp/scratch doctor` still reads `~/.bfinger/config`.
  For an isolated run pass `-config` and `-home`, or set `BFINGER_HOME`, which
  moves both.
- **`-home` overrides a `known_keys` line in the file**, because the flag
  recomputes the pin store path; `BFINGER_HOME` does not. Pass `-known-keys`
  beside `-home` to keep the file's store.
- **`quorum = 0`** in the file or environment exits 2, while `-quorum 0` is
  ignored, because zero is the flag's "not set" value.

`bfinger doctor` prints what a combination of file, environment and flags
resolved to: the home, pin store, header source, node, facade and settlement
leg.

## Keys

| Key | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `home` | `-home` | `~/.bfinger` | the state directory: pin store, identity, wallet, state, journal |
| `host` | `-host` | none | the overlay host to look up at. Unset, the domain's manifest names it (`ls_finger`). Required to look up by identity key |
| `header_url` | `-header-url` | none | the header source every proof is checked against (see [Header sources](#header-sources)). Needed by lookups, `verify`, `keys verify`, `receive`, `fund -txid`, and a publisher without a node |
| `network` | none | `main` | `main`, `test` or `regtest` (any private chain). Sets the proof-of-work floor and the fund address prefix. A `woc:` source must name the same network |
| `known_keys` | `-known-keys` | `<home>/known_keys` | the pin store ([format](known-keys.md)) |
| `quorum` | `-quorum` | `1` | lookups: how many hosts must answer identically ([host selection](host-selection.md)) |
| `timeout` | `-timeout` | `15s` | every request, as a Go duration |
| `facade` | none | none | every sending command and `publish -resume`: the overlay host base URL the objects are posted to (`<facade>/submit`). `publish -facade` overrides it for one run |
| `settle` | none | none | every sending command: `tcp:<host:port>` (bare EF to an ingress, no acknowledgement), `rpc:<url>` (a node that acknowledges, with `rpc_user` and `rpc_pass`) or `arcade:<url>` (any ARC-compatible API; bfinger posts to `<url>/tx`) |
| `arcade_key` | none | none | bearer token for the `arcade:` URL, sent nowhere else |
| `proofs` | none | `wait` | `wait`: a transition returns once its token is mined and proven (a wait that runs out after the token was sent continues as `async`). `async`: once the network has accepted it; a later command collects the proof. `async` refuses `settle = tcp:` unless `funding = wallet` |
| `rpc` | none | none | the node's JSON-RPC URL |
| `asset` | none | none | the node's asset HTTP API (tips and proofs) |
| `rpc_user` | none | `bitcoin` | the node's basic-auth user |
| `rpc_pass` | none | `bitcoin` | the node's basic-auth password |
| `topic` | none | `tm_finger` | the topic objects are published to, and the one `domain-docs` names in the manifest |
| `originator` | none | `bfinger` | the BRC-100 originator string presented to a wallet |
| `wallet` | none | `embedded` | `embedded`: bfinger's own key and coin in the home. `wire`: a BRC-100 wallet on this machine at `wallet_url` signs; coin stays in the home |
| `wallet_url` | none | `http://127.0.0.1:3301` | the wire wallet's base URL. Loopback only: the wire carries no authentication |
| `funding` | none | `home` | `home`: fees come from the home's coin and bfinger settles. `wallet`: the wire wallet funds, signs and broadcasts every mined transaction; needs `wallet = wire`, a node, and `-yes` (never `-dry-run`) on every sending command |

`network`, `wallet`, `funding` and `proofs` accept only the values listed, and
`timeout` and `quorum` must parse; anything else exits 2 naming the key.

**When a node is needed.** `rpc` and `asset` are both required by `fund`
without `-txid` (mining or `-rescan`, coinbase: only on a regtest chain you
run, for development and tests) and by every sending command, except that with
`settle = arcade:<url>`, `proofs = async` and `header_url` set, a publisher
needs no node: the tip comes from the header source and the proof from the ARC
service (`kill` and `pay` poll it for their proofs). `funding = wallet`
still needs a node. `fund -txid` reads the transaction from `asset` when it is set,
otherwise from WhatsOnChain (`main` and `test` only).

## Header sources

Every proof is checked against the block headers of one source, so the source
is the root of trust for every VERIFIED.

| `header_url` | Source | What is checked |
| --- | --- | --- |
| `woc:main`, `woc:test` | the public WhatsOnChain API | each header is hashed here and must carry the work its bits claim; on `main` it must also claim at least difficulty 4e9 |
| `chaintracks:https://host/v2` | a chaintracks v2 service, public or your own | the same, at the configured `network` |
| `https://host:port` | an overlay bridge's header read API (`GET /v1/tip`, `GET /v1/root/{height}`) | nothing further: it serves roots, not headers, so run it yourself |

A source that serves headers cannot lie about a root without mining a header at
the floor, about 1.7e19 hashes on mainnet. For the strongest check, run your
own chaintracks service or node. A header that fails the check is an error
(exit 2), never a refusal: a lying source is not a failed proof.

## Environment variables

| Variable | Effect |
| --- | --- |
| `BFINGER_<KEY>` | one per key, for example `BFINGER_HEADER_URL`, `BFINGER_RPC_PASS`. Overrides the file; a flag overrides it |
| `BFINGER_HOME` | the state directory **and** where the config file is looked for |
| `XDG_CONFIG_HOME` | the third place the config file is looked for |
| `HOME` | the default home is `$HOME/.bfinger` |
| `LC_ALL`, `LC_CTYPE`, `LANG` | the first one set decides whether the terminal is UTF-8; if it names no UTF-8 encoding, `-ascii` is the default. None set is UTF-8 |

`NO_COLOR` and `TERM` are not consulted: bfinger adds no color of its own, and
a record's color is shown only with `-ansi`, honored as typed.

## Global flags

These go before the address or command word; parsing stops at the first word
that is not a flag. A reader flag given before the address is moved after it,
so `bfinger -ansi alice@example.com` and `bfinger alice@example.com -ansi` are
the same command.

| Flag | Default | Notes |
| --- | --- | --- |
| `-config PATH` | see [The config file](#the-config-file) | |
| `-home DIR` | `~/.bfinger` | key `home` |
| `-host URL` | the domain's manifest | key `host` |
| `-header-url SOURCE` | none | key `header_url` |
| `-known-keys PATH` | `<home>/known_keys` | key `known_keys` |
| `-quorum N` | `1` | key `quorum` |
| `-timeout DUR` | `15s` | key `timeout` |
| `-v` | `false` | print every verification step on stderr |
| `-json` | `false` | machine output, stable schema |
| `-version` | `false` | print the version and exit, before the config file is read |
| `-h`, `-help`, `--help` | | print the usage and exit 0, before the config file is read |

## Reader flags

For a lookup (`bfinger <acct|identity-key>`) and `verify <acct>`, on either
side of the address. `-json` and `-v` are also global flags.

| Flag | Default | Notes |
| --- | --- | --- |
| `-l` | `false` | the full record: full key, fingerprint, txids, window, stores, refs, hosts |
| `-field NAME` | none | print one body field, or a store of that name, and nothing else. Prints nothing when the field is missing or on a refusal, so branch on the exit status |
| `-json` | `false` | one indented object. Wins over `-field` |
| `-v` | `false` | the step trace on stderr; `verify` always prints it |
| `-yes` | `false` | accept a first-contact pin without prompting. Required in a script |
| `-accept-unmined` | `true` for a lookup, `false` for `verify` | exit 0 on `VERIFIED-UNMINED`. Turn off with `-accept-unmined=false` |
| `-ansi` | `false` | let a record's color through: SGR sequences only, re-validated, each colored value ending with a reset |
| `-ascii` | `false`, or `true` when the locale is not UTF-8 | print characters above 7 bits as `?` |
| `-watch` | `false` | poll and print every change until interrupted |
| `-interval DUR` | `15s` | the poll interval with `-watch` |
| `-once` | `false` | with `-watch`, exit after the first change, with that answer's exit status |

## `keys` flags

Flags go on either side of the address. `list` and `verify` take none; `-h` or
`help` after either prints the usage.

| Subcommand | Flag | Default | Notes |
| --- | --- | --- | --- |
| `trust <acct>` | `-key HEX` | none, required | a 33-byte compressed key, `02` or `03`, a point on the curve in its one encoding |
| `trust <acct>` | `-fingerprint SHA256:...` | none | refuse unless the key hashes to this |
| `trust <acct>` | `-force` | `false` | replace an existing or retired pin |
| `forget <acct>` | `-all` | `false` | remove the `@rotated-from` history too |

## Owner command flags

The sending commands (`create`, `status`, `rotate`, `retire`, `kill`, `pay`)
spend real funds and send nothing without `-yes`: they build, print and
discard, saying so on stderr. Under `funding = wallet` they refuse to run
without `-yes` or with `-dry-run` (exit 2).

| Command | Flag | Default | Notes |
| --- | --- | --- | --- |
| `fund` | `-txid TXID` | none | import the outputs of a mined payment you sent to the fund address from your own wallet, after checking its proof against `header_url`; mines nothing. The way to fund a wallet on mainnet and testnet |
| `fund` | `-blocks N` | `101` | coinbase: only on a regtest chain you run (development and tests). Blocks to mine to the fund address; coinbase matures after 100 more |
| `fund` | `-batch N` | `30` | coinbase: only on a regtest chain you run (development and tests). Blocks per `generatetoaddress` call |
| `fund` | `-rescan` | `false` | coinbase: only on a regtest chain you run (development and tests). Re-read the last `-blocks` blocks for coinbase this wallet holds, instead of mining |
| `create`, `status`, `rotate` | `-set k=v`, `-set k=@file` | none, repeatable | set a body field, from a file with `@` |
| `create`, `status`, `rotate` | `-unset k` | none, repeatable | remove a body field |
| `create`, `status`, `rotate` | `-store name=text`, `-store name=@file` | none, repeatable | publish the value as a sub-record in its own carrier and commit to it from the record |
| `create`, `status`, `rotate` | `-unstore name` | none, repeatable | drop a sub-store from the record |
| `create`, `status`, `rotate` | `-expires DUR` | `0`, unbounded | the validity window's upper bound, from now |
| `create`, `status`, `rotate` | `-funding-count N` | `16` | outputs per funding tree when one is minted |
| `create`, `status`, `rotate` | `-funding-sats N` | `1` | satoshis per funding output |
| `create`, `status`, `rotate` | `-yes` | `false` | **enables the spend** |
| `create`, `status`, `rotate` | `-dry-run` | `false` | build and print, send nothing |
| `rotate` | `-successor HEX` | a fresh key file; with `wallet = wire`, the wallet's identity | the identity key to hand over to |
| `retire` | `-confirm RETIRE` | none, required | compared literally |
| `retire` | `-yes`, `-dry-run` | `false` | as above |
| `kill` | `-confirm KILL` | none, required | compared literally |
| `kill` | `-yes` | `false` | **enables the spend**; without it each sweep's hex is printed and nothing is sent |
| `pay <acct\|key> <sats>` | `-yes`, `-dry-run` | `false` | as above |
| `publish` | `-resume` | `false`, required | re-send the object leg for the current state |
| `publish` | `-facade URL` | the `facade` key | send to another facade for this run |
| `domain-docs <handle@domain>` | `-host URL` | none, required | the overlay host's https base URL, as readers reach it; no path |
| `domain-docs <handle@domain>` | `-key HEX` | the home's identity | the identity key the handle resolves to |
| `domain-docs <handle@domain>` | `-out DIR` | `bfinger-site` | where `manifest.json` and `.well-known/metanet-handles/by-handle/<handle>.json` are written; an existing manifest is updated |
| `serve-wallet` | `-listen ADDR` | `127.0.0.1:3301` | must be a loopback address |
| `init`, `doctor`, `receive <notice>` | none | | each answers `-h` and `help` without doing any work |

[examples.md](examples.md) shows the commands with their output, and
[self-host.md](self-host.md) uses `domain-docs`.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | verified, or the command did what it was asked |
| `1` | refused or not found; the printed code says which |
| `2` | usage, configuration or transport failure |

A run never exits 0 on an answer it did not verify.

## In a container

The image is distroless and runs as `nonroot` (uid 65532), so the default home
is `/home/nonroot/.bfinger`. The image carries that directory, empty, owned by
`nonroot` with mode 0700, so a new named volume mounted there starts writable:

```console
$ docker run --rm -v bfinger-state:/home/nonroot/.bfinger \
    -e BFINGER_HEADER_URL=woc:main \
    ghcr.io/lightwebinc/bfinger alice@example.com -yes
```

- **Configuration** comes from `BFINGER_*` variables (`-e`, or `--env-file`
  for anything secret), from flags after the image name, or from a file named
  `config` in the state volume (elsewhere, name it with `-config`).
- **A bind mount** keeps the host directory's owner, so run as that owner and
  name the home, because a uid the image does not know has no home directory:
  `--user "$(id -u):$(id -g)" -v "$PWD/state:/state" -e BFINGER_HOME=/state`.
- **Addresses are seen from inside the container.** `127.0.0.1` is the
  container itself, so a service on the Docker host needs an address the
  container can reach, or `--network host`.
- **`wallet = wire` and `serve-wallet` are loopback only**, which inside a
  container means the same container; a wire wallet on the Docker host needs
  `--network host`.

[user-guide.md](user-guide.md#running-in-a-container) has worked commands.

## The overlay host modules

`tm_finger` and `ls_finger` have no configuration of their own. The host loads
them with two of its settings, `OVERLAY_TOPICS` (must include `tm_finger`) and
`OVERLAY_MODULES` (the absolute path of the bundle). The `finger-host` image
sets both. [host/README.md](../host/README.md#deploying) covers building and
loading the bundle; [self-host.md](self-host.md) covers the image.

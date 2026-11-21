# walletd

A BRC-100 wallet from the BSV wallet toolbox, served over the wallet wire on
loopback, so that bfinger with `wallet = wire` signs, funds and broadcasts
through a real wallet.

A separate Go module on purpose: the toolbox brings a dependency tree the
command must not carry, and the wire is the only thing the two share. The
`replace` lines in `go.mod` hold its transitive tree at versions that build
together, and Renovate leaves them alone. Move them by hand, with the toolbox:

- `kube-openapi`: to the commit the toolbox's own `go.mod` replaces it with.
- `quic-go`, `webtransport-go` and `go-libp2p`: as one set that builds. The
  toolbox's `go.mod` has no replace for them, so there is none to copy;
  `go build` decides.

```console
$ cd tools/walletd && go build -o walletd .
$ ./walletd
walletd: main wallet, identity 03…, serving the wire on http://127.0.0.1:3301
```

On `main` and `test` the toolbox uses its default public services. For a
private chain, `-network tstn -arcade http://192.0.2.10:8080` points it at
an Arcade installation and its chaintracks instead.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-dir` | `~/.bfinger-walletd` | the root key (`key.hex`, generated at 0600 on first run) and the SQLite storage |
| `-listen` | `127.0.0.1:3301` | loopback only; the wire carries no authentication |
| `-network` | `main` | `main`, `test`, `ttn` or `tstn` |
| `-arcade` | none | `tstn`: the Arcade base URL, for broadcast and merkle proofs |
| `-chaintracks` | `<arcade>/chaintracks` | `tstn`: the go-chaintracks v2 base URL, without `/v2` |

On `tstn` the wallet talks to nothing but Arcade and chaintracks: there is no
block explorer on a private chain, so there is nothing else to configure.

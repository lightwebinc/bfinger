# mintprobe

The measurement harness behind the committed-record design. A separate Go
module so the repository's direct-dependency rule (go-sdk and bcommon, nothing
else) stays true for the binary; run it from this directory:

```
GOWORK=off go run .            # mint matrix, delegate mint, new shapes
GOWORK=off go run . golden     # the CBOR goldens record/ is checked against
```

What it prints, and what each proved (go-sdk v1.5.2):

- `main.go`: six `pushdrop.Lock` mints across counterparty × forSelf at the
  ratified triple; only `Anyone` + `forSelf=true` is both reader-derivable
  and self-verifying.
- `delegate.go`: a token minted by a delegate's wallet derives its locking
  key from the delegate, never the subject.
- `newshape.go`: the committed-record shapes: state token 146 bytes
  (`[tag, C]`), carrier output 292 bytes around a 182-byte deterministic-CBOR
  record. Uses `fxamacker/cbor` here only: bfinger's records are written by a
  minimal codec (package `cbor` of `github.com/lightwebinc/bcommon`, through
  `internal/protocol/record/`), and `go run . golden` prints the reference
  bytes that codec is checked against.

# The known-keys file

`~/.bfinger/known_keys`, mode `0600` in a `0700` directory, written atomically
(temp file in the same directory, then `rename`).

**This file owns the grammar.** It is meant to have more than one reader (this
tool and any independent verifier), and two readers of an unspecified format
is how a pin silently fails open.
`internal/reader/knownkeys/testdata/known_keys.sample` is the contract: one
record of each form, held to this implementation by `TestSampleParses`. Vendor
those exact bytes into any other reader of the file, so a grammar change one
side accepts and the other refuses fails a test rather than a user.

## Grammar

One record per line, whitespace-separated. A line whose first non-blank
character is `#` is a comment (there are no trailing comments); blank lines are
ignored. Three record forms:

```
<address> secp256k1 <33-byte-key-hex> [key=value ...]
@rotated-from <address> secp256k1 <33-byte-key-hex> [key=value ...]
@retired <address> secp256k1 <33-byte-key-hex> [key=value ...]
```

Recognized `key=value` fields: `seq`, `until_seq` (unsigned decimal),
`first`, `last`, `at` (RFC 3339), `fp` (fingerprint).

The key is 33 bytes of hex whose first byte is `02` or `03`, the compressed
form, in its one encoding: an `x` below the field prime that names a point on
the curve. All of it is checked on read, so a file holding any other key is
refused, naming the line. A key whose `x` is at or above the prime is a second
spelling of another key, and pins are compared as strings. A new pin is
written in lower-case hex.

## Rules that are not style

- **An unknown field is refused, not ignored.** An old binary that silently
  drops a field a newer one wrote is two readers disagreeing about what the
  same pin means. The refusal names the field.
- **At most one active record per address.** A file holding two is refused,
  naming the address and both lines. Two active pins for one address is a
  store that quietly holds two answers to the question it exists to answer,
  and whichever one a reader happens to take first decides who it trusts.
- **An uncompressed key is refused.** A key that decodes to 33 bytes but
  begins `04` can never match a key derived from a record, so accepting it
  would store a pin that refuses everything for a reason no message explains.
- **A malformed line is an error, not a skip.** Skipping drops a pin and
  leaves the tool trusting first contact again for that address, which is
  precisely the failure this file prevents, arriving silently.
- **`@rotated-from` is history and never satisfies a pin.** A superseded key
  that still matches is not a rotation; it is a second valid key for one
  identity.
- **`@retired` is a pin that REFUSES, not the absence of a pin.** Collapsing
  the two would let a retired identity be trusted again on first contact.
- **The fingerprint is unpadded base64** of SHA-256 over the 33-byte
  compressed key, prefixed `SHA256:`. Unpadded because it is compared by eye at
  first contact and trailing `=` is what people drop when retyping it.

## Why the file is line-oriented

It is read by a human at the one moment that matters: first contact, when the
tool prints a fingerprint and asks whether to trust it. A format somebody can
`grep`, read in a terminal and edit with an editor they already have is worth
more here than one that is tidier to parse.

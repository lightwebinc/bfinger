# Security

Please report a vulnerability privately through GitHub: the repository's
**Security** tab, **Report a vulnerability**. Do not open a public issue for
it.

A report is most useful with the version (`bfinger -version`), what you ran,
what you expected, and what happened instead. We aim to acknowledge within
three working days.

In scope: anything that lets a reader print `VERIFIED` for an answer that is
not the owner's current record, accept a key it should refuse, or leak a
private key; anything that lets a host admit or serve a record the rules in
[docs/committed-record.md](docs/committed-record.md) refuse; and the release
artefacts and images.

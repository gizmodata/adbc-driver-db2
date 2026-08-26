# AGENTS.md

Project notes for Codex and other contributors (same content as CLAUDE.md).

## What this is

A pure-Go [Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver for
IBM Db2 that implements the DRDA wire protocol itself (no IBM CLI, no
JCC), distributed as a Go module, a c-shared library, and a Python wheel
(`pip install adbc-driver-db2`). Sibling of `gizmodata/adbc-driver-quack`
(same layout, CI shape, and packaging conventions).

## Layout

```
internal/ddm    DSS framing, DDM code points, EBCDIC, parameter helpers
internal/drda   connection/handshake/auth, SQLCA/SQLDA parsing, FD:OCA row
                decoding, streaming Query, SQLDTA parameter encoding
driver/db2      ADBC Driver/Database/Connection/Statement, Arrow mapping,
                metadata (SYSCAT), bulk ingest, options, SQL shims
pkg/db2         cgo c-shared exports (generated-template style, from quack)
python/         adbc_driver_db2 package + tests
cmd/drda-sniff  decoding TCP proxy for protocol debugging
```

## Reference material (not in repo)

`~/LocalOnly/git/db2-ref/`: Apache Derby (client `org.apache.derby.client.net`
and server `impl/drda` — the most complete open DRDA implementation),
`pydrda` (small Python Db2 client), IBM JCC jar + `Probe.java` (run real
IBM traffic through `cmd/drda-sniff` to see what Db2 expects). The Open
Group publishes the DRDA V5 spec (free registration).

## Testing

- `go test ./...` — hermetic unit tests only.
- With `DB2_HOST=localhost` (plus optional `DB2_PORT/DB2_DATABASE/DB2_USER/DB2_PASSWORD`):
  DRDA integration tests, ADBC driver tests, and `TestValidation`, the
  upstream arrow-adbc conformance suite. Keep it passing.
- Python: build the c-shared lib, copy it into `python/adbc_driver_db2/`,
  `pip install -e ".[test]"`, `DB2_HOST=localhost pytest`. The
  `test_db2_to_gizmosql.py` test also needs `GIZMOSQL_URI` (+ token or
  password) and mirrors the customer notebook flow this driver exists for.
- Local servers: `icr.io/db2_community/db2` (amd64; ~10 min first start;
  offers SECMEC 3 only) and `gizmodata/gizmosql` (`TLS_ENABLED=0`,
  URI `gizmosql://localhost:31337?transport=tcp`).

## Protocol facts that are not in the public spec (learned empirically)

- Db2 LUW's `SQLDAGRP` carries 10 undocumented bytes between `SQLCCSID`
  and `SQLDOPTGRP`; column names live in `SQLNAME`, `SQLUNNAMED=1` for
  expressions. See `parseSQLDAGRP`.
- A `QRYDTA` row is an `SQLCADTA`: nullable SQLCA (a *warning* SQLCA,
  e.g. 01003, can precede a real row), then the SQLDTAGRP indicator.
- `BOOLEAN` flows as 2 bytes; `(VAR)GRAPHIC` lengths are in characters
  (×2 bytes, UTF-16BE); rows may be split across query blocks.
- Parameters: strings go as `NVARMIX 0x7FFF` (UTF-8), small binary as
  `NVARBINARY 0x7FFF`; >32 KiB LOBs use descriptor `C9/CF 80 09`, a 9-byte
  placeholder (`0x02` + 8-byte BE length) and `EXTDTA` (null byte + data).
  `FDODTA` begins with a 0x00 row indicator.
- A second `EXCSAT` with `CCSIDMGR 1208` must follow `ACCRDB`, or
  `PKGNAMCSN` is interpreted as EBCDIC (SQL0805N with garbage tokens).
- `RDBUPDRM` (0x2218) is informational, not an error.

## Conventions

- Go 1.25 floor, Arrow Go v18, arrow-adbc v1.11 (match quack).
- Options: `adbc.db2.<noun>`; driver name "ADBC Db2 Driver - Go".
- Keep `CHANGELOG.md` `[Unreleased]` current; semver tags `vX.Y.Z`;
  bump `driver/db2/version.go` and `python/adbc_driver_db2/_version.py`
  together.
- Never `panic` across the cgo boundary (globalPoison pattern in pkg/db2).
- Prefer keyword arguments in Python examples and tests.

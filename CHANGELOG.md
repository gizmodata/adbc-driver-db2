# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.10] - 2026-08-27

### Added

- `NOTICE` file strengthened with an explicit no-confidential-materials /
  independent-reimplementation statement and a trademark section; it now
  ships inside the wheel (`dist-info/licenses/`).
- README "Provenance & licensing" and "Trademarks" sections and a
  not-affiliated-with-IBM disclaimer.

## [0.1.9] - 2026-08-27

### Fixed
- Db2 for i result descriptors (SQLDARD) were parsed as having no columns,
  which produced an empty Arrow table (0.1.8: a "descriptor mismatch"
  error). The SQLDA layout is now selected per server product: Db2 for i
  sends a null SQLCA, a populated SQLDHGRP with six additional bytes, eight
  (not ten) bytes after SQLCCSID, and two (not three) trailing group
  indicators. Verified against a captured V7R5 trace (108 columns).

## [0.1.8] - 2026-08-27

### Fixed
- Character data from Db2 for i / z/OS is decoded in the server's CCSIDs
  (single-byte CHAR/VARCHAR in CCSIDSBC, e.g. EBCDIC 37; mixed types in
  CCSIDMBC; graphic in CCSIDDBC) instead of being assumed UTF-8 — EBCDIC
  blanks showed up as `@`, letters were garbled. CCSIDs 37/500 (and their
  euro variants) are converted exactly; SQLDA names and SQLCA messages use
  the same rule.
- A result whose SQLDARD column count differs from the QRYDSC field count
  is reported as an error instead of yielding an empty table.

### Changed
- Trace: hex dumps up to 64 KiB (the SQLDARD of wide tables), and the
  parsed column / field counts are logged.

## [0.1.7] - 2026-08-27

### Added
- `adbc.db2.trace_file` (also `?trace_file=`) writes the DRDA trace to a
  file, and `adbc.db2.trace=hex` dumps reply payloads — for diagnosing a
  server from a notebook, where the process's stderr is not visible. The
  trace now also records the driver version, server attributes, and the
  row count decoded from each query block.

### Changed
- A `CNTQRY` reply carrying neither rows nor `ENDQRYRM` is retried a few
  times before the result set is treated as exhausted.

## [0.1.6] - 2026-08-27

### Added
- Zoned decimal (`NUMERIC` on Db2 for i / z/OS, FD:OCA 0x10/0x11 and the
  numeric-character form 0x12/0x13) decoding and parameter encoding; the
  driver failed with "unsupported FD:OCA type 0x10" on such columns.
- Null-terminated (CSTR / NTERMBYTE) and 1-byte-length (LSTR / PSCLBYTE)
  string and byte forms.

## [0.1.5] - 2026-08-27

### Fixed
- Queries opened cursors on package section 65, which is not bound as a
  cursor; Db2 for i refuses that with OPNQFLRM ("open query failure"). All
  statements now use section 1 (a WITH HOLD cursor section in SYSSH200).
- When a reply message such as OPNQFLRM is accompanied by an SQLCA, the
  SQLCA's SQLCODE / message is reported instead of the bare reply code.

## [0.1.4] - 2026-08-27

### Added
- Automatic package binding: when the dynamic-SQL package (default
  `NULLID.SYSSH200`) does not exist on the server — the normal state on Db2
  for i and Db2 for z/OS, which do not ship the CLI packages — the driver
  binds it exactly as IBM's `DB2Binder` does (`BGNBND` / `BNDSQLSTT` /
  `ENDBND`, 65 sections, WITH HOLD cursors) and retries. New options
  `adbc.db2.package` (`COLLECTION.PKGID`, also `?package=` in the URI) and
  `adbc.db2.no_auto_bind`.

### Fixed
- A failed command in a chained request (e.g. a prepare error) could leave
  the driver waiting forever for replies the server never sends.
- The package consistency token is sent as raw ASCII bytes (as JCC does)
  even when the server keeps DDM names in EBCDIC.

## [0.1.3] - 2026-08-27

### Fixed
- Servers that do not accept `CCSIDMGR 1208` (Db2 for i / z/OS) read the
  package name in EBCDIC; the driver kept sending it as ASCII, so every
  statement failed with SQL0805N (package `NULLID.SYSSH200` not found, with
  garbled tokens). The driver now honours the server's EXCSATRD answer and
  encodes `PKGNAMCSN` in EBCDIC when 1208 is not granted.

## [0.1.2] - 2026-08-26

### Added
- Integration test and README section for Columnar's DuckDB `adbc` community
  extension: `read_adbc`, `ATTACH 'profile://…'`, and INSERT / CTAS into Db2
  through ADBC bulk ingest, using the driver manifest + a connection profile.

### Fixed
- String and binary parameter values bound via `Bind`/`BindStream` could be
  read after the bound record was released (garbage or empty values).

### Changed
- README and the DuckDB/GizmoSQL integration test use the `adbc_scanner`
  `CREATE SECRET` + `ATTACH` pattern (query Db2 as an attached catalog) instead
  of `SET VARIABLE`/`adbc_connect`.
- DRDA trace (`adbc.db2.trace`) now includes the SQL text of each statement.

## [0.1.1] - 2026-08-26

### Fixed
- SQLSTATE / SQLERRPROC sent in EBCDIC (Db2 for z/OS, Db2 for i) were passed
  through undecoded; the Python driver manager then failed with
  "bytes must be in range(0, 256)" instead of reporting the SQL error.
  Both are now decoded per the server's CCSID, and the C ABI never emits
  a non-ASCII SQLSTATE.
- macOS wheel is built with `MACOSX_DEPLOYMENT_TARGET=12.0` so it installs
  on macOS 12+ instead of only the runner's release.

## [0.1.0] - 2026-08-26

Built with Go 1.26.7, arrow-adbc v1.12.0, arrow-go v18.7.0.

### Added
- Pure-Go DRDA client for Db2 (`internal/ddm`, `internal/drda`): DSS
  framing with large-object continuation, EBCDIC (CCSID 500) codec,
  EXCSAT/ACCSEC/SECCHK/ACCRDB handshake, SECMEC 3 (cleartext), 4 (user id
  only) and 9 (Diffie-Hellman + DES encrypted credentials), TLS, server
  type-definition (endianness / CCSID) negotiation.
- Streaming cursors (`OPNQRY`/`CNTQRY`) with rows split across query
  blocks handled, warning SQLCAs per row, and LOB data via `EXTDTA`.
- FD:OCA decoding for every Db2 LUW column type: integers, DECIMAL
  (packed), DECFLOAT (DPD), REAL/DOUBLE, BOOLEAN, CHAR/VARCHAR/GRAPHIC
  (UTF-8 / UTF-16), DATE/TIME/TIMESTAMP(0–12), BINARY/VARBINARY, BLOB/CLOB.
- Parameter binding (`SQLDTA`) encoded the way IBM's JCC driver does,
  including out-of-line BLOB/CLOB parameters, with many rows pipelined
  per round trip.
- ADBC driver (`driver/db2`): `db2://` URIs and `adbc.db2.*` options,
  Arrow record-batch streaming, `Bind`/`BindStream`, `GetParameterSchema`,
  `ExecuteSchema`, bulk ingest (create / append / replace / create_append,
  declared temporary tables, auto-sized VARCHARs), transactions and
  isolation levels, `GetInfo` / `GetObjects` (with primary/foreign keys) /
  `GetTableSchema` / `GetTableTypes` from `SYSCAT`, `GetSetOptions`,
  SQLSTATE/SQLCODE error mapping. Passes the `apache/arrow-adbc` Go
  conformance suite.
- Compatibility shims: `SELECT` without `FROM` gets `SYSIBM.SYSDUMMY1`;
  untyped parameter markers (SQL0418N) are retried as
  `CAST(? AS <bound Arrow type>)`.
- cgo c-shared driver library (`pkg/db2`) exporting `AdbcDriverInit` /
  `Db2DriverInit`, and the `adbc-driver-db2` Python wheel with a DBAPI
  module, driver-manifest installer (`python -m adbc_driver_db2 install-manifest`)
  for connection profiles.
- `cmd/drda-sniff`: transparent DRDA-decoding proxy for protocol work.
- Test suites: Go unit + integration tests, Python integration tests, and
  a Db2 → GizmoSQL streaming-ingest test (ADBC to ADBC).

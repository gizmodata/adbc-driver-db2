# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

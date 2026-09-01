# adbc-driver-db2

[<img src="https://img.shields.io/badge/GitHub-gizmodata%2Fadbc--driver--db2-blue.svg?logo=Github">](https://github.com/gizmodata/adbc-driver-db2)
[![CI](https://github.com/gizmodata/adbc-driver-db2/actions/workflows/ci.yml/badge.svg)](https://github.com/gizmodata/adbc-driver-db2/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/gizmodata/adbc-driver-db2.svg)](https://pkg.go.dev/github.com/gizmodata/adbc-driver-db2)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gizmodata/adbc-driver-db2)](go.mod)
[![Supported Python Versions](https://img.shields.io/pypi/pyversions/adbc-driver-db2)](https://pypi.org/project/adbc-driver-db2/)
[![PyPI version](https://badge.fury.io/py/adbc-driver-db2.svg)](https://badge.fury.io/py/adbc-driver-db2)
[![PyPI Downloads](https://img.shields.io/pepy/dt/adbc-driver-db2.svg)](https://pypi.org/project/adbc-driver-db2/)
[![License](https://img.shields.io/github/license/gizmodata/adbc-driver-db2)](LICENSE)

A **pure-Go [Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver for
IBM Db2** that speaks Db2's native wire protocol,
[DRDA](https://en.wikipedia.org/wiki/DRDA), directly.

No IBM CLI / ODBC
driver, no `db2jcc`, no cgo dependency on IBM libraries — one
statically-linked shared library that plugs into every ADBC language
binding (Python, Go, R, C/C++, C#, Rust, JavaScript) and returns
[Arrow](https://arrow.apache.org/) record batches.

> An independent, open-source project. Not affiliated with, endorsed by, or sponsored by IBM Corporation. "IBM" and "Db2" are trademarks of International Business Machines Corporation, used here only to describe compatibility.

```
pip install adbc-driver-db2
```

```python
import adbc_driver_db2.dbapi as db2

with db2.connect(
    uri="db2://db2host:50000/SAMPLE",
    username="db2inst1",
    password="********",
) as conn, conn.cursor() as cur:
    cur.execute("SELECT * FROM SALES.ORDERS WHERE ORDER_DATE >= ?", parameters=("2024-01-01",))
    table = cur.fetch_arrow_table()        # or cur.fetch_record_batch() to stream
```

> **Status: alpha.** Tested against Db2 LUW 12.1 (Community Edition).
> The DRDA implementation parses the server's type definition, so Db2
> for z/OS and Db2 for i should work in principle but have not been
> exercised yet — reports welcome.

## Why

* **Arrow-native, streaming.** Result sets are pulled one DRDA query
  block at a time (1 MiB by default) and surfaced as Arrow record
  batches, so a 100 M-row `SELECT` needs about one batch of client
  memory. Perfect for `Db2 → Arrow → somewhere else` pipelines.
* **Zero-install.** IBM's clients are large, click-wrap licensed
  downloads. This is a `pip install` / `go get`.
* **The full ADBC feature set**: queries, parameter binding, bulk
  ingest (`adbc_ingest` with create/append/replace/create_append and
  declared temporary tables), transactions and isolation levels,
  `GetObjects`/`GetTableSchema`/`GetInfo` catalog metadata for tools
  like DBeaver, connection profiles, and the ADBC driver manifest.
  The driver passes the `apache/arrow-adbc` Go conformance suite.

## Connection URI and options

```
db2://[user[:password]@]host[:port]/DATABASE[?param=value&...]
```

| URI parameter / ADBC option | Meaning |
|---|---|
| `tls=true` / `adbc.db2.tls` | TLS (Db2's SSL port is conventionally 50001; required for Db2 on Cloud) |
| `tls_ca_cert=/path.pem` / `adbc.db2.tls.ca_cert` | CA bundle for self-signed servers |
| `tls_skip_verify=true` / `adbc.db2.tls.skip_verify` | Skip certificate verification |
| `secmec=9` / `adbc.db2.security_mechanism` | DRDA security mechanism: `9` encrypted user id + password (default when the server allows it), `3` cleartext password (inside TLS this is fine), `4` user id only. An explicit setting **fails closed** — the connection errors instead of silently downgrading when the server refuses it. SECMEC protects the credentials only; use TLS to encrypt data in transit. The read-only connection option `adbc.db2.security_mechanism_active` reports what was actually negotiated. |
| `schema=NAME` / `adbc.db2.current_schema` | `SET CURRENT SCHEMA` after connecting |
| `query_block_size=N` / `adbc.db2.query_block_size` | DRDA `QRYBLKSZ` in bytes (default 1 MiB) |
| `batch_size=N` / `adbc.db2.batch_size` | Max rows per Arrow record batch (default 65536) |
| `batch_bytes=N` / `adbc.db2.batch_bytes` | Approximate max bytes per Arrow record batch (default 8 MiB, which keeps batches under Flight SQL's 16 MiB gRPC cap; 0 = only `batch_size` applies) |
| `connect_timeout=30` / `adbc.db2.connect_timeout` | Seconds or Go duration |
| `application_name=X` / `adbc.db2.application_name` | Reported to the server |
| `package=COLL.PKG` / `adbc.db2.package` | Dynamic-SQL package (default `NULLID.SYSSH200`); bound automatically if missing (`adbc.db2.no_auto_bind=true` disables) |
| `trace=true|hex` / `adbc.db2.trace` | Log every DRDA message (`hex` adds payload dumps) |
| `trace_file=/path` / `adbc.db2.trace_file` | Write the trace to a file instead of stderr (use from notebooks) |

Standard ADBC options also apply: `username`, `password`,
`adbc.connection.autocommit`, `adbc.connection.transaction.isolation_level`
(mapped to `SET CURRENT ISOLATION` UR/CS/RS/RR), `adbc.connection.catalog`,
`adbc.connection.db_schema`, and the `adbc.ingest.*` statement options.

## Python

### Streaming large result sets

```python
with db2.connect(uri=uri, username=user, password=pw) as conn, conn.cursor() as cur:
    cur.execute("SELECT * FROM BIG.TABLE")
    reader = cur.fetch_record_batch()          # pyarrow.RecordBatchReader
    for batch in reader:                       # one query block at a time
        process(batch)
```

### Db2 → GizmoSQL (or any ADBC target), ADBC to ADBC

The reader above can be handed straight to another driver's bulk ingest,
so a table moves from Db2 into [GizmoSQL](https://gizmodata.com/gizmosql)
without ever being materialised on the client:

```python
import adbc_driver_db2.dbapi as db2
import adbc_driver_gizmosql.dbapi as gizmosql

with db2.connect(uri=db2_uri, username=db2_user, password=db2_pw) as src, \
     gizmosql.connect(gizmosql_uri, username="token", password=token) as dst:
    with src.cursor() as s, dst.cursor() as d:
        s.execute("SELECT * FROM SALES.ORDERS")
        rows = d.adbc_ingest(table_name="orders", data=s.fetch_record_batch(), mode="replace")
        dst.commit()
        print(f"Loaded {rows:,} rows")
```

**Batch sizing.** Each Arrow record batch becomes one Flight SQL `DoPut`
message on the GizmoSQL side, and the GizmoSQL driver's gRPC client caps
messages at 16 MiB by default. The Db2 driver's batches are Flight-safe
out of the box: a batch is capped at 65,536 rows (`batch_size`) **and**
8 MiB (`batch_bytes`), so even very wide tables stream through without
tripping the cap. Both knobs are tunable (URI parameter or `adbc.db2.*`
in `db_kwargs`):

```python
src = db2.connect(uri=db2_uri + "?batch_bytes=4194304", username=db2_user, password=db2_pw)
# or, equivalently:
src = db2.connect(uri=db2_uri, db_kwargs={"adbc.db2.batch_bytes": str(4 * 1024 * 1024)},
                  username=db2_user, password=db2_pw)
```

`batch_bytes=0` removes the byte cap (batches then bound only by
`batch_size` rows — a pre-0.2 default that could exceed 16 MiB for rows
wider than ~825 bytes and fail ingest with `ResourceExhausted: trying to
send message larger than max`). To move *more* than 8 MiB per message,
also raise the GizmoSQL client's gRPC cap with
`adbc.flight.sql.client_option.with_max_msg_size` — see the
[GizmoSQL driver README](https://github.com/gizmodata/gizmosql-adbc#tuning-bulk-ingest-batch-size).

### Bulk ingest (Arrow → Db2)

```python
import pyarrow as pa

table = pa.table({"id": [1, 2, 3], "name": ["alpha", "beta", None]})
with db2.connect(uri=uri, username=user, password=pw, autocommit=True) as conn, conn.cursor() as cur:
    cur.adbc_ingest("new_table", table, mode="create")   # create | append | replace | create_append
```

Rows are pipelined many per DRDA round trip (1000 by default;
`adbc.db2.ingest.batch_rows`). `VARCHAR`/`VARBINARY` columns of a created
table are sized from the first batch (`adbc.db2.ingest.varchar_length`
overrides) because Db2's row-size limit depends on the tablespace page
size. Values over 32 KiB are sent as out-of-line BLOB/CLOB data.

### pandas and Polars

```python
with db2.connect(uri=uri, username=user, password=pw) as conn, conn.cursor() as cur:
    cur.execute("SELECT * FROM SYSCAT.TABLES")
    df = cur.fetch_df()                                   # pandas
    # or, zero-copy into Polars:
    import polars as pl
    cur.execute("SELECT * FROM SYSCAT.COLUMNS")
    pl_df = pl.from_arrow(cur.fetch_arrow_table())
```

### Parameters and executemany

```python
import datetime

with db2.connect(uri=uri, username=user, password=pw, autocommit=True) as conn, conn.cursor() as cur:
    cur.execute("CREATE TABLE EVENTS (ID INTEGER NOT NULL, NAME VARCHAR(40), AT TIMESTAMP)")
    cur.executemany(
        "INSERT INTO EVENTS VALUES (?, ?, ?)",
        [(1, "start", datetime.datetime(2024, 1, 1, 9, 0)), (2, "stop", None)],
    )   # rows are pipelined many-per-round-trip, not sent one at a time
    cur.execute("SELECT NAME FROM EVENTS WHERE ID = ?", parameters=(2,))
    print(cur.fetchone())
```

### Query Db2 live from DuckDB or GizmoSQL (`adbc_scanner`)

The c-shared driver plugs straight into DuckDB's
[`adbc_scanner`](https://github.com/Query-farm/adbc_scanner) community
extension (see the [GizmoSQL guide](https://docs.gizmosql.com/adbc_scanner_duckdb/)) — and therefore into [GizmoSQL](https://gizmodata.com/gizmosql),
which embeds DuckDB. Store the credentials in a DuckDB secret once, then
`ATTACH` Db2 like any other database and query it with plain SQL
(projection and filter pushdown included):

```sql
INSTALL adbc_scanner FROM community;
LOAD adbc_scanner;

CREATE SECRET db2_secret (
    TYPE adbc,
    SCOPE 'db2://db2host:50000/SAMPLE',
    driver 'db2',                        -- by name after `python -m adbc_driver_db2 install-manifest`,
                                         -- or a path: '/path/to/libadbc_driver_db2.so'
    uri 'db2://db2host:50000/SAMPLE',
    username 'db2inst1',
    password '********'
);

ATTACH 'db2://db2host:50000/SAMPLE' AS db2 (TYPE adbc);

SELECT * FROM db2.SALES.ORDERS WHERE ORDER_DATE >= DATE '2024-01-01';

-- join Db2 with local data without copying it first
SELECT o.ORDER_ID, c.name
FROM db2.SALES.ORDERS o
JOIN customers c ON c.id = o.CUST_ID;

-- materialise a local copy in DuckDB
CREATE TABLE orders AS SELECT * FROM db2.SALES.ORDERS;

-- push (the simple way): write straight into Db2 through the attached
-- catalog with plain SQL — USE the Db2 schema, then CREATE TABLE ... AS
USE db2.DB2INST1;
CREATE TABLE ORDERS_COPY AS SELECT * FROM memory.local_orders;   -- CTAS into Db2
USE memory;
```

For arbitrary Db2 SQL (or to push data the other way) the secret also
drives the function API:

```sql
SET VARIABLE db2 = adbc_connect({'secret': 'db2_secret'});
SELECT * FROM adbc_scan(getvariable('db2')::BIGINT, 'SELECT * FROM SYSCAT.TABLES FETCH FIRST 10 ROWS ONLY');
SELECT * FROM adbc_insert(getvariable('db2')::BIGINT, 'ORDERS_COPY2', (SELECT * FROM local_orders), mode := 'create');
```

Both write paths work: `USE <attached schema>; CREATE TABLE ... AS ...` (and
`INSERT INTO ...`) through the attached catalog, or the `adbc_insert()`
function for arbitrary relations.

Credentials can live in a self-contained DuckDB secret (above) or in an ADBC
connection profile — `adbc_scanner` resolves `profile://…` URIs too, so
profiles are not specific to any one extension (the connection-profiles
section below shows the profile setup).

### Query Db2 from DuckDB via connection profiles (Columnar's `adbc` extension)

Columnar's [`adbc`](https://github.com/columnar-tech/duckdb-adbc-client)
community extension (see the [GizmoSQL guide](https://docs.gizmosql.com/adbc_duckdb_extension/))
resolves databases through ADBC
[connection profiles](https://arrow.apache.org/adbc/main/format/connection_profiles.html),
and additionally supports writing (`INSERT`, `CREATE TABLE AS`) into the
attached database through ADBC bulk ingest. Install this driver's manifest
once, write a profile, and Db2 is a catalog:

```sh
python -m adbc_driver_db2 install-manifest        # registers driver "db2"
cat > ~/.config/adbc/profiles/warehouse.toml <<EOF   # macOS: ~/Library/Application Support/ADBC/Profiles/
profile_version = 1
driver = "db2"

[Options]
uri = "db2://db2host:50000/SAMPLE"
username = "db2inst1"
password = "********"
EOF
```

```sql
INSTALL adbc FROM community;
LOAD adbc;

SELECT * FROM read_adbc('profile://warehouse', 'SELECT * FROM SALES.ORDERS FETCH FIRST 10 ROWS ONLY');

ATTACH 'profile://warehouse' AS db2 (TYPE adbc);
USE db2.SALES;
SELECT COUNT(*) FROM ORDERS;
CREATE TABLE ORDERS_2024 AS SELECT * FROM memory.staged_orders;   -- bulk ingest into Db2
INSERT INTO ORDERS_2024 SELECT * FROM memory.late_orders;
```

Both DuckDB extensions are exercised in this repo's test suite
(`python/tests/test_adbc_scanner.py`, `python/tests/test_duckdb_adbc_client.py`).

### Alternative: drive `adbc_driver_manager` directly

```python
from adbc_driver_manager import dbapi
import adbc_driver_db2

conn = dbapi.connect(
    driver=adbc_driver_db2._driver_path(),
    entrypoint="Db2DriverInit",
    db_kwargs={"uri": "db2://host:50000/SAMPLE", "username": "u", "password": "p"},
)
```

### Connection profiles and the driver manifest

```
python -m adbc_driver_db2 install-manifest
```

writes a `db2.toml` ADBC driver manifest so the driver resolves by name
from any ADBC consumer — `adbc_driver_manager.dbapi.connect(uri="db2://...")`,
DuckDB's `adbc_connect({'driver': 'db2', ...})`, DBeaver's ADBC
connection type — and from connection profiles:

```toml
# ~/.config/adbc/profiles/warehouse.toml
driver   = "db2"
uri      = "db2://db2host:50000/SAMPLE?schema=SALES"
username = "reporting"
password = "********"
```

```python
from adbc_driver_manager import dbapi
conn = dbapi.connect(profile="warehouse")
```

## Go

```go
import (
    "github.com/apache/arrow-adbc/go/adbc"
    "github.com/apache/arrow-go/v18/arrow/memory"
    "github.com/gizmodata/adbc-driver-db2/driver/db2"
)

drv := db2.NewDriver(memory.DefaultAllocator)
database, _ := drv.NewDatabase(map[string]string{
    adbc.OptionKeyURI:      "db2://host:50000/SAMPLE",
    adbc.OptionKeyUsername: "db2inst1",
    adbc.OptionKeyPassword: "********",
})
conn, _ := database.Open(ctx)
stmt, _ := conn.NewStatement()
stmt.SetSqlQuery("SELECT * FROM SYSCAT.TABLES")
reader, _, _ := stmt.ExecuteQuery(ctx)
for reader.Next() { rec := reader.RecordBatch(); ... }
```

Bulk ingest from Go:

```go
stmt, _ := conn.NewStatement()
stmt.SetOption(adbc.OptionKeyIngestTargetTable, "ORDERS_COPY")
stmt.SetOption(adbc.OptionKeyIngestMode, adbc.OptionValueIngestModeCreateAppend)
stmt.BindStream(ctx, reader)          // any array.RecordReader — e.g. from Parquet, Flight, or another ADBC driver
rows, _ := stmt.ExecuteUpdate(ctx)
```

The `internal/drda` package is a self-contained DRDA client (connect,
describe, execute, streaming cursors, parameter binding, LOBs) that the
ADBC layer sits on.

## Type mapping

| Db2 | Arrow |
|---|---|
| SMALLINT / INTEGER / BIGINT | int16 / int32 / int64 |
| DECIMAL(p,s), NUMERIC | decimal128(p,s) |
| DECFLOAT(16/34) | utf8 (exact text; no fixed scale) |
| REAL / DOUBLE | float32 / float64 |
| BOOLEAN | bool |
| CHAR, VARCHAR, LONG VARCHAR, (VAR)GRAPHIC, CLOB, DBCLOB, XML | utf8 |
| BINARY, VARBINARY, BLOB, ROWID | binary |
| DATE / TIME | date32 / time32[s] |
| TIMESTAMP(p) | timestamp[s/ms/us/ns] by precision |

Every field carries `db2:type`, `db2:length`, `db2:precision`,
`db2:scale` metadata; a schema produced by this driver round-trips
through bulk ingest with the original Db2 types.

## Development

```
go test ./...                                   # unit tests (no server needed)
DB2_HOST=localhost go test ./...                # integration + ADBC conformance suite
go build -buildmode=c-shared -tags driverlib -o pkg/db2/libadbc_driver_db2.dylib ./pkg/db2
ADBC_DB2_LIBRARY=$PWD/pkg/db2/libadbc_driver_db2.dylib pip install -e ".[test]"
DB2_HOST=localhost pytest
```

A Db2 for testing: `docker run -d -p 50000:50000 --privileged -e LICENSE=accept -e DB2INST1_PASSWORD=password -e DBNAME=testdb icr.io/db2_community/db2`
(first start takes ~10 minutes). `go run ./cmd/drda-sniff` is a
transparent proxy that decodes DRDA traffic — handy when comparing this
driver's messages with IBM's own clients.

## Provenance & licensing

This is an **independent, from-scratch reimplementation** of a client for
IBM Db2's DRDA network protocol, written in Go. It contains **no IBM
Corporation source code** and links against **no IBM Corporation libraries**
— that's the whole point (no IBM CLI/ODBC, no `db2jcc`). DRDA (Distributed
Relational Database Architecture) is an **open standard published by
[The Open Group](https://www.opengroup.org/)**; network protocols and the
interfaces needed for interoperability are not proprietary to any vendor.

The implementation was written from the public DRDA specification and by
reference to **openly licensed** source — no confidential specification,
non-public documentation, or binary disassembly was used:

- [Apache Derby](https://db.apache.org/derby/) (**Apache-2.0**) — its network
  client and server DRDA implementations.
- [pydrda](https://github.com/nakagami/pydrda) (**MIT**) — a pure-Python Db2
  DRDA client; parts of the Db2-specific message encoding were reimplemented
  by reference to it.
- [Apache Arrow ADBC](https://github.com/apache/arrow-adbc) and
  [Apache Arrow Go](https://github.com/apache/arrow-go) (**Apache-2.0**) — the
  ADBC framework and Arrow libraries.

See [`NOTICE`](NOTICE) for full attribution.

## Trademarks

IBM and Db2 are trademarks or registered trademarks of International Business
Machines Corporation. **adbc-driver-db2 is not affiliated with, endorsed by,
or sponsored by IBM Corporation.** References to "IBM Db2" identify the
software this driver interoperates with and are nominative fair use.

## License

[MIT](LICENSE) — Copyright (c) 2026 GizmoData LLC. See [`NOTICE`](NOTICE) for
third-party attributions.

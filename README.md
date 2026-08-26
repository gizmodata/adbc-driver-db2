# adbc-driver-db2

[<img src="https://img.shields.io/badge/GitHub-gizmodata%2Fadbc--driver--db2-blue.svg?logo=Github">](https://github.com/gizmodata/adbc-driver-db2)
[![CI](https://github.com/gizmodata/adbc-driver-db2/actions/workflows/ci.yml/badge.svg)](https://github.com/gizmodata/adbc-driver-db2/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/gizmodata/adbc-driver-db2.svg)](https://pkg.go.dev/github.com/gizmodata/adbc-driver-db2)
[![License](https://img.shields.io/github/license/gizmodata/adbc-driver-db2)](LICENSE)

A **pure-Go [Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver for
IBM Db2** that speaks Db2's native wire protocol,
[DRDA](https://en.wikipedia.org/wiki/DRDA), directly. No IBM CLI / ODBC
driver, no `db2jcc`, no cgo dependency on IBM libraries — one
statically-linked shared library that plugs into every ADBC language
binding (Python, Go, R, C/C++, C#, Rust, JavaScript) and returns
[Arrow](https://arrow.apache.org/) record batches.

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
| `secmec=9` / `adbc.db2.security_mechanism` | DRDA security mechanism: `9` encrypted user id + password (default when the server allows it), `3` cleartext password (inside TLS this is fine), `4` user id only |
| `schema=NAME` / `adbc.db2.current_schema` | `SET CURRENT SCHEMA` after connecting |
| `query_block_size=N` / `adbc.db2.query_block_size` | DRDA `QRYBLKSZ` in bytes (default 1 MiB) |
| `batch_size=N` / `adbc.db2.batch_size` | Max rows per Arrow record batch (default 65536) |
| `connect_timeout=30` / `adbc.db2.connect_timeout` | Seconds or Go duration |
| `application_name=X` / `adbc.db2.application_name` | Reported to the server |
| `adbc.db2.trace=true` | Log every DRDA message to stderr |

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
        s.execute("SELECT * FROM PFWF6076.CGIBASE")
        rows = d.adbc_ingest(table_name="cgibase", data=s.fetch_record_batch(), mode="replace")
        dst.commit()
        print(f"Loaded {rows:,} rows")
```

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

writes a `db2.toml` ADBC driver manifest so `adbc_driver_manager.dbapi.connect(uri="db2://...")`
and connection profiles (`driver = "db2"`) resolve this wheel's driver by
name.

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

## License

MIT — see [LICENSE](LICENSE). DRDA is an open standard published by
[The Open Group](https://www.opengroup.org/); this implementation was
written from the specification and the open-source
[Apache Derby](https://db.apache.org/derby/) and
[pydrda](https://github.com/nakagami/pydrda) clients.

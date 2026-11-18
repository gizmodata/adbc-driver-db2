# adbc-driver-db2

**Python package for [adbc-driver-db2](https://github.com/gizmodata/adbc-driver-db2), a pure-Go
[Apache Arrow ADBC](https://arrow.apache.org/adbc/) driver for IBM Db2, by
[GizmoData™](https://gizmodata.com).**

It speaks Db2's native wire protocol, [DRDA](https://en.wikipedia.org/wiki/DRDA), directly and
returns Arrow record batches. No IBM CLI / ODBC driver, no `db2jcc`, no IBM libraries: the driver
is a shared library inside the wheel.

> An independent, open-source project. Not affiliated with, endorsed by, or sponsored by IBM
> Corporation. "IBM" and "Db2" are trademarks of International Business Machines Corporation,
> used here only to describe compatibility.

> **Status: alpha.** Tested against Db2 LUW 12.1 (Community Edition).

This page covers the Python package. For other languages (C, C++, Go, Rust, R, DuckDB), the
one-line installer of the shared library (`curl https://install.adbc.gizmodata.com/db2 | sh`),
every connection option, type mapping, bulk ingest and the Hardcharger and DuckDB integrations,
see the [project README on GitHub](https://github.com/gizmodata/adbc-driver-db2#readme).

## Install

```bash
pip install adbc-driver-db2
```

- Python 3.10 or later.
- Wheels for Linux x86_64 and aarch64 (glibc 2.17 or later), macOS arm64 (13 or later) and
  Windows x64.
- Depends on `adbc-driver-manager` and `pyarrow`.

For a local database, IBM's Db2 Community Edition container (x86_64, first start takes about ten
minutes) gives you `db2://localhost:50000/testdb` as `db2inst1` / `password`:

```bash
docker run -d --name db2 --privileged=true -p 50000:50000 \
  -e LICENSE=accept -e DB2INST1_PASSWORD=password -e DBNAME=testdb \
  icr.io/db2_community/db2
```

## Connect and query

```python
import adbc_driver_db2.dbapi as db2

with db2.connect(
    uri="db2://localhost:50000/testdb",
    username="db2inst1",
    password="password",
) as conn, conn.cursor() as cur:
    cur.execute("SELECT 42 AS answer, 'hello db2' AS greeting FROM SYSIBM.SYSDUMMY1")
    table = cur.fetch_arrow_table()  # a pyarrow.Table
    print(table)
```

The result is a `pyarrow.Table`: pass it to Polars, pandas, DuckDB or anything else that reads
Arrow.

## Parameters

```python
import adbc_driver_db2.dbapi as db2

with db2.connect(
    uri="db2://localhost:50000/testdb",
    username="db2inst1",
    password="password",
) as conn, conn.cursor() as cur:
    cur.execute(
        "SELECT TABNAME FROM SYSCAT.TABLES WHERE TABSCHEMA = ? FETCH FIRST 3 ROWS ONLY",
        parameters=("SYSCAT",),
    )
    print(cur.fetchall())
```

## Stream large results

`fetch_record_batch()` returns a `pyarrow.RecordBatchReader` that pulls one DRDA query block at a
time, so memory stays bounded however large the result is:

```python
import adbc_driver_db2.dbapi as db2

with db2.connect(
    uri="db2://localhost:50000/testdb",
    username="db2inst1",
    password="password",
) as conn, conn.cursor() as cur:
    cur.execute("SELECT * FROM SYSCAT.COLUMNS")
    rows = 0
    for batch in cur.fetch_record_batch():
        rows += batch.num_rows
    print(rows)
```

## Load the driver by name (driver manifest)

ADBC [driver manifests](https://arrow.apache.org/adbc/current/format/driver_manifests.html) let
the ADBC driver manager, and [connection profiles](https://arrow.apache.org/adbc/current/format/connection_profiles.html),
find a driver by name. The package writes one for its own library:

```console
$ python -m adbc_driver_db2 install-manifest
Wrote ADBC driver manifest: .../etc/adbc/drivers/db2.toml
```

Then the driver manager resolves `db2://` URLs with no import of `adbc_driver_db2`:

```python
from adbc_driver_manager import dbapi

with dbapi.connect(
    uri="db2://localhost:50000/testdb",
    db_kwargs={"username": "db2inst1", "password": "password"},
) as conn, conn.cursor() as cur:
    cur.execute("SELECT 1 AS one FROM SYSIBM.SYSDUMMY1")
    print(cur.fetch_arrow_table())
```

## More

- [Project README](https://github.com/gizmodata/adbc-driver-db2#readme): every option and
  feature, other languages, and the one-line installer for the shared library
- [Changelog](https://github.com/gizmodata/adbc-driver-db2/blob/main/CHANGELOG.md)
- [Issues](https://github.com/gizmodata/adbc-driver-db2/issues)

Licensed under the [MIT License](https://github.com/gizmodata/adbc-driver-db2/blob/main/LICENSE).

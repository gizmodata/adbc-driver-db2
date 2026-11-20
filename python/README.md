# adbc-driver-db2 - by [GizmoData™](https://gizmodata.com)

<p>
  <a href="https://github.com/gizmodata/adbc-driver-db2"><img src="https://img.shields.io/badge/GitHub-gizmodata%2Fadbc--driver--db2-blue.svg?logo=Github" alt="GitHub gizmodata/adbc-driver-db2"></a>
  <a href="https://github.com/gizmodata/adbc-driver-db2/actions/workflows/ci.yml"><img src="https://github.com/gizmodata/adbc-driver-db2/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://pkg.go.dev/github.com/gizmodata/adbc-driver-db2"><img src="https://pkg.go.dev/badge/github.com/gizmodata/adbc-driver-db2.svg" alt="Go Reference"></a>
  <a href="https://github.com/gizmodata/adbc-driver-db2/blob/main/go.mod"><img src="https://img.shields.io/github/go-mod/go-version/gizmodata/adbc-driver-db2" alt="Go version"></a>
  <a href="https://pypi.org/project/adbc-driver-db2/"><img src="https://img.shields.io/pypi/pyversions/adbc-driver-db2" alt="Supported Python versions"></a>
  <a href="https://pypi.org/project/adbc-driver-db2/"><img src="https://img.shields.io/pypi/v/adbc-driver-db2?logo=pypi" alt="PyPI version"></a>
  <a href="https://pepy.tech/projects/adbc-driver-db2"><img src="https://img.shields.io/pepy/dt/adbc-driver-db2.svg" alt="PyPI downloads"></a>
  <a href="https://github.com/gizmodata/adbc-driver-db2/tree/main/validation"><img src="https://img.shields.io/badge/ADBC-conformance%20suites%20passing-2ea44f.svg" alt="ADBC conformance suites passing"></a>
  <a href="https://adbc-drivers.org/building-drivers/style-guide.html"><img src="https://img.shields.io/badge/ADBC%20style%20guide-conforms-2ea44f.svg" alt="Follows the ADBC driver style guide"></a>
  <a href="https://github.com/gizmodata/adbc-driver-db2/blob/main/LICENSE"><img src="https://img.shields.io/github/license/gizmodata/adbc-driver-db2" alt="MIT license"></a>
  <a href="https://gizmodata.com/hardcharger"><img src="https://img.shields.io/badge/works%20with-Hardcharger%E2%84%A2-f15a24.svg" alt="Works with Hardcharger"></a>
</p>

<h3 align="center">Arrow straight from IBM Db2, in Python: a pure-Go ADBC driver that speaks DRDA. No IBM client needed.</h3>

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

## Options

Driver options go in `db_kwargs`; `adbc_driver_db2.DatabaseOptions` names them all. They follow
the [ADBC driver style guide](https://adbc-drivers.org/building-drivers/style-guide.html):
`db2.*`, with query behaviour under `db2.query.*` and bulk ingest under `db2.ingest.*`.

```python
import adbc_driver_db2.dbapi as db2
from adbc_driver_db2 import DatabaseOptions

with db2.connect(
    uri="db2://localhost:50000/testdb",
    username="db2inst1",
    password="password",
    db_kwargs={
        DatabaseOptions.BATCH_SIZE.value: "10000",  # "db2.query.batch_rows"
        DatabaseOptions.CURRENT_SCHEMA.value: "SYSCAT",  # "db2.session.current_schema"
    },
) as conn, conn.cursor() as cur:
    cur.execute("SELECT COUNT(*) AS n FROM TABLES")
    print(cur.fetchone())
```

Version 0.3.0 renamed the options (`adbc.db2.batch_size` is now `db2.query.batch_rows`, and so
on; the [project README](https://github.com/gizmodata/adbc-driver-db2#connection-uri-and-options) has the full list). The old names still
work, and the first use of each writes one deprecation line to stderr.

## More

- [Project README](https://github.com/gizmodata/adbc-driver-db2#readme): every option and
  feature, other languages, and the one-line installer for the shared library
- [Changelog](https://github.com/gizmodata/adbc-driver-db2/blob/main/CHANGELOG.md)
- [Issues](https://github.com/gizmodata/adbc-driver-db2/issues)

Licensed under the [MIT License](https://github.com/gizmodata/adbc-driver-db2/blob/main/LICENSE).

---

<p align="center">
  <a href="https://gizmodata.com"><img src="https://raw.githubusercontent.com/gizmodata/adbc-driver-db2/main/docs/assets/gizmodata-logo.png" alt="GizmoData™" width="56"></a><br>
  <sub>Built by <a href="https://gizmodata.com">GizmoData™</a>, makers of <a href="https://gizmodata.com/hardcharger">Hardcharger™</a>. Free and open source.</sub>
</p>

<p align="center"><sub>Hardcharger™ and GizmoData™ are trademarks of GizmoData LLC. IBM and Db2 are trademarks of International Business Machines Corporation.</sub></p>

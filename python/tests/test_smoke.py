"""End-to-end ADBC tests against a live Db2.

Several tests mirror snippets in the project README — if the README
claims a pattern works, a test here proves it.
"""

from __future__ import annotations

import datetime
import decimal

import pyarrow as pa
import pytest

pytestmark = pytest.mark.integration


def _connect(server, **kwargs):
    import adbc_driver_db2.dbapi as db2

    return db2.connect(
        uri=server.uri, username=server.user, password=server.password, **kwargs
    )


def test_readme_quickstart_connect_and_query(db2_server):
    """README "Connect and query"."""
    with _connect(db2_server) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT 42 AS answer, 'hello db2' AS greeting FROM SYSIBM.SYSDUMMY1"
        )
        table = cur.fetch_arrow_table()
        assert table.num_rows == 1
        assert table.column("ANSWER").to_pylist() == [42]
        assert table.column("GREETING").to_pylist() == ["hello db2"]


def test_credentials_in_uri(db2_server):
    import adbc_driver_db2.dbapi as db2

    with db2.connect(uri=db2_server.uri_with_credentials) as conn, conn.cursor() as cur:
        cur.execute("SELECT CURRENT SERVER FROM SYSIBM.SYSDUMMY1")
        (row,) = cur.fetchall()
        assert row[0].strip().upper() == db2_server.database.upper()


def test_readme_alternative_manager_pattern(db2_server):
    """README "Alternative: drive adbc_driver_manager directly"."""
    from adbc_driver_manager import dbapi

    import adbc_driver_db2

    with dbapi.connect(
        driver=adbc_driver_db2._driver_path(),
        entrypoint="Db2DriverInit",
        db_kwargs={
            "uri": db2_server.uri,
            "username": db2_server.user,
            "password": db2_server.password,
        },
    ) as conn, conn.cursor() as cur:
        cur.execute("SELECT 42 AS answer FROM SYSIBM.SYSDUMMY1")
        assert cur.fetch_arrow_table().to_pylist() == [{"ANSWER": 42}]


def test_readme_streaming_large_result_set(db2_server):
    """README "Streaming large result sets" — fetch_record_batch loop."""
    with _connect(db2_server) as conn, conn.cursor() as cur:
        cur.execute(
            """WITH T(N) AS (VALUES 1 UNION ALL SELECT N + 1 FROM T WHERE N < 100000)
               SELECT N, CAST('row-' || CAST(N AS VARCHAR(10)) AS VARCHAR(20)) AS S FROM T"""
        )
        reader = cur.fetch_record_batch()
        total = 0
        batches = 0
        for batch in reader:
            total += batch.num_rows
            batches += 1
        assert total == 100_000
        assert batches > 1


def test_types_round_trip(db2_server):
    with _connect(db2_server, autocommit=True) as conn, conn.cursor() as cur:
        try:
            cur.execute("DROP TABLE ADBC_PY_TYPES")
        except Exception:
            pass
        cur.execute(
            """CREATE TABLE ADBC_PY_TYPES (
                 ID INTEGER NOT NULL, SI SMALLINT, BI BIGINT, D DECIMAL(12,3), R REAL, F DOUBLE,
                 C CHAR(5), VC VARCHAR(50), DT DATE, TM TIME, TS TIMESTAMP(6), B BOOLEAN,
                 VB VARBINARY(16), BL BLOB(100000), CL CLOB(1000))"""
        )
        try:
            cur.execute(
                """INSERT INTO ADBC_PY_TYPES VALUES
                   (1, 7, 9223372036854775807, 123456789.125, 1.5, 2.25, 'ab', 'héllo wörld',
                    '2024-02-29', '13:45:59', '2024-02-29-13.45.59.123456', TRUE,
                    BX'DEADBEEF', BLOB(X'CAFEBABE'), 'clob text'),
                   (2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)"""
            )
            cur.execute("SELECT * FROM ADBC_PY_TYPES ORDER BY ID")
            tbl = cur.fetch_arrow_table()
            assert tbl.num_rows == 2
            row = tbl.slice(0, 1).to_pylist()[0]
            assert row["SI"] == 7
            assert row["BI"] == 9223372036854775807
            assert row["D"] == decimal.Decimal("123456789.125")
            assert row["R"] == 1.5
            assert row["F"] == 2.25
            assert row["C"] == "ab"
            assert row["VC"] == "héllo wörld"
            assert row["DT"] == datetime.date(2024, 2, 29)
            assert row["TM"] == datetime.time(13, 45, 59)
            assert row["TS"] == datetime.datetime(2024, 2, 29, 13, 45, 59, 123456)
            assert row["B"] is True
            assert row["VB"] == b"\xde\xad\xbe\xef"
            assert row["BL"] == b"\xca\xfe\xba\xbe"
            assert row["CL"] == "clob text"
            nulls = tbl.slice(1, 1).to_pylist()[0]
            assert all(v is None for k, v in nulls.items() if k != "ID")
            assert tbl.schema.field("D").type == pa.decimal128(12, 3)
            assert tbl.schema.field("TS").type == pa.timestamp("us")
        finally:
            cur.execute("DROP TABLE ADBC_PY_TYPES")


def test_readme_bulk_ingest(db2_server):
    """README "Bulk ingest (Arrow → Db2)"."""
    import adbc_driver_db2.dbapi as db2

    table = pa.table(
        {
            "id": pa.array([1, 2, 3], pa.int32()),
            "name": ["alpha", "beta", None],
            "amount": pa.array([decimal.Decimal("1.25"), decimal.Decimal("2.50"), None], pa.decimal128(10, 2)),
            "when": pa.array([datetime.datetime(2024, 1, 1, 12, 0, 0)] * 3, pa.timestamp("us")),
        }
    )
    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
    ) as conn, conn.cursor() as cur:
        try:
            cur.execute('DROP TABLE "adbc_py_ingest"')
        except Exception:
            pass
        n = cur.adbc_ingest("adbc_py_ingest", table, mode="create")
        assert n == 3
        n = cur.adbc_ingest("adbc_py_ingest", table, mode="append")
        assert n == 3
        cur.execute('SELECT COUNT(*), COUNT("name"), SUM("amount") FROM "adbc_py_ingest"')
        assert cur.fetchone() == (6, 4, decimal.Decimal("7.50"))
        cur.execute('DROP TABLE "adbc_py_ingest"')


def test_parameters(db2_server):
    with _connect(db2_server, autocommit=True) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT N FROM (VALUES 1, 2, 3) AS T(N) WHERE N > ? ORDER BY N", parameters=(1,)
        )
        assert [r[0] for r in cur.fetchall()] == [2, 3]


def test_transactions(db2_server):
    with _connect(db2_server) as conn:
        with conn.cursor() as cur:
            try:
                cur.execute("DROP TABLE ADBC_PY_TX")
            except Exception:
                pass
            conn.commit()
            cur.execute("CREATE TABLE ADBC_PY_TX (ID INTEGER)")
            conn.commit()
            cur.execute("INSERT INTO ADBC_PY_TX VALUES (1), (2)")
            conn.rollback()
            cur.execute("INSERT INTO ADBC_PY_TX VALUES (3)")
            conn.commit()
            cur.execute("SELECT COUNT(*) FROM ADBC_PY_TX")
            assert cur.fetchone() == (1,)
            cur.execute("DROP TABLE ADBC_PY_TX")
            conn.commit()


def test_metadata(db2_server):
    with _connect(db2_server, autocommit=True) as conn:
        info = conn.adbc_get_info()
        assert info["vendor_name"] == "IBM Db2"
        assert info["driver_name"] == "ADBC Db2 Driver - Go"
        objects = conn.adbc_get_objects(depth="tables", db_schema_filter="SYSCAT", table_name_filter="TABLES").read_all()
        assert objects.num_rows == 1
        schema = conn.adbc_get_table_schema("TABLES", db_schema_filter="SYSCAT")
        assert "TABNAME" in schema.names


def test_error_reporting(db2_server):
    import adbc_driver_db2.dbapi as db2

    with _connect(db2_server) as conn, conn.cursor() as cur:
        with pytest.raises(db2.Error) as excinfo:
            cur.execute("SELECT * FROM NO_SUCH_TABLE_XYZ")
        assert "SQLCODE=-204" in str(excinfo.value)
        assert "42704" in str(excinfo.value)

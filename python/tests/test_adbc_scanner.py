"""Tier-3 integration test: DuckDB (and therefore GizmoSQL, which embeds
DuckDB) scanning Db2 *live* through the ``adbc_scanner`` community
extension, loading this repo's c-shared driver library.

    duckdb  ──adbc_scanner──▶  libadbc_driver_db2  ──DRDA──▶  Db2

Skipped when the ``duckdb`` package or the extension is unavailable.
"""

from __future__ import annotations

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.duckdb_ext]


def test_duckdb_adbc_scan_pulls_from_db2(db2_server):
    duckdb = pytest.importorskip("duckdb")
    import adbc_driver_db2
    import adbc_driver_db2.dbapi as db2

    # A small user table to scan (SYSCAT tables are wide and slow to describe).
    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
    ) as c, c.cursor() as cur:
        try:
            cur.execute("DROP TABLE ADBC_SCAN_T")
        except Exception:
            pass
        cur.execute("CREATE TABLE ADBC_SCAN_T (ID INTEGER NOT NULL, NAME VARCHAR(20))")
        cur.execute("INSERT INTO ADBC_SCAN_T SELECT N, 'n' || CAST(N AS VARCHAR(5)) FROM (VALUES 1,2,3,4,5,6,7,8,9) AS T(N)")

    con = duckdb.connect(database=":memory:")
    try:
        con.execute(query="INSTALL adbc_scanner FROM community")
        con.execute(query="LOAD adbc_scanner")
    except Exception as exc:  # pragma: no cover - environment dependent
        pytest.skip(reason=f"adbc_scanner extension unavailable: {exc}")

    # Credentials live in a DuckDB secret; ATTACH then exposes Db2 as a
    # catalog (README "Query Db2 live from DuckDB or GizmoSQL").
    con.execute(
        query="""CREATE SECRET db2_secret (
            TYPE adbc,
            SCOPE $uri,
            driver $driver,
            entrypoint 'Db2DriverInit',
            uri $uri,
            username $username,
            password $password
        )""",
        parameters={
            "driver": adbc_driver_db2._driver_path(),
            "uri": db2_server.uri,
            "username": db2_server.user,
            "password": db2_server.password,
        },
    )
    # ATTACH takes no bind parameters; the URI is a literal.
    con.execute(query=f"ATTACH '{db2_server.uri}' AS db2 (TYPE adbc)")

    # Pull through the storage extension. Two things to know about Db2's
    # catalog: schema names are stored blank-padded ('SYSCAT  '), which
    # Db2 ignores in comparisons but DuckDB does not, hence TRIM; and
    # COUNT(*) on a table whose first column is a string trips an
    # adbc_scanner bug (fix in progress), hence COUNT(TABNAME).
    n, first_schema = con.execute(
        query="SELECT COUNT(TABNAME), MIN(TABSCHEMA) FROM db2.SYSCAT.TABLES WHERE TRIM(TABSCHEMA) = 'SYSCAT'"
    ).fetchone()
    assert n > 100
    assert first_schema.strip() == "SYSCAT"

    # Projection + filter pushdown against a user table (int-first, so
    # COUNT(*) is fine here).
    n, = con.execute(query="SELECT COUNT(*) FROM db2.DB2INST1.ADBC_SCAN_T WHERE ID > 5").fetchone()
    assert n == 4

    # Types survive the trip: Db2 DECIMAL/TIMESTAMP become DuckDB DECIMAL/TIMESTAMP.
    kinds = {name: typ for name, typ, *_ in con.execute(query="DESCRIBE db2.SYSCAT.TABLES").fetchall()}
    assert kinds["TABSCHEMA"] == "VARCHAR"
    assert kinds["CREATE_TIME"].startswith("TIMESTAMP")
    assert kinds["CARD"] == "BIGINT"

    # The same secret drives the function API for arbitrary Db2 SQL.
    con.execute(query="SET VARIABLE db2 = adbc_connect({'secret': 'db2_secret'})")
    typed = con.execute(
        query="""DESCRIBE SELECT * FROM adbc_scan(getvariable('db2')::BIGINT,
                 'SELECT CAST(1.25 AS DECIMAL(10,2)) AS D, CURRENT TIMESTAMP AS TS FROM SYSIBM.SYSDUMMY1')"""
    ).fetchall()
    fkinds = {name: typ for name, typ, *_ in typed}
    assert fkinds["D"].startswith("DECIMAL")
    assert fkinds["TS"].startswith("TIMESTAMP")

    con.execute(query="DETACH db2")
    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
    ) as c, c.cursor() as cur:
        cur.execute("DROP TABLE ADBC_SCAN_T")
    con.execute(query="SELECT adbc_disconnect(getvariable('db2')::BIGINT)")
    con.close()


def test_duckdb_writes_to_db2(db2_server):
    """DuckDB writes back into Db2 through adbc_scanner two ways:

    ``USE db2.<schema>; CREATE TABLE ... AS SELECT`` through the attached
    catalog (plain SQL, no function call), and the ``adbc_insert`` function.
    Both bulk-ingest a DuckDB relation into a brand-new Db2 table.
    """
    duckdb = pytest.importorskip("duckdb")
    import adbc_driver_db2
    import adbc_driver_db2.dbapi as db2

    ctas_table = "ADBC_CTAS_WRITE_T"
    insert_table = "ADBC_INSERT_WRITE_T"

    def drop(*names):
        with db2.connect(
            uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
        ) as c, c.cursor() as cur:
            for name in names:
                try:
                    cur.execute(f"DROP TABLE {name}")
                except Exception:
                    pass

    drop(ctas_table, insert_table)

    con = duckdb.connect(database=":memory:")
    try:
        try:
            con.execute(query="INSTALL adbc_scanner FROM community")
            con.execute(query="LOAD adbc_scanner")
        except Exception as exc:  # pragma: no cover - environment dependent
            pytest.skip(reason=f"adbc_scanner extension unavailable: {exc}")

        con.execute(
            query="""CREATE SECRET db2_secret (
                TYPE adbc,
                SCOPE $uri,
                driver $driver,
                entrypoint 'Db2DriverInit',
                uri $uri,
                username $username,
                password $password
            )""",
            parameters={
                "driver": adbc_driver_db2._driver_path(),
                "uri": db2_server.uri,
                "username": db2_server.user,
                "password": db2_server.password,
            },
        )
        con.execute(query=f"ATTACH '{db2_server.uri}' AS db2 (TYPE adbc)")

        rows = 2_000
        # Quote the aliases uppercase so the columns land as Db2's idiomatic
        # ID/NAME (adbc_scanner preserves DuckDB's column case into Db2, and
        # DuckDB lowercases unquoted identifiers).
        con.execute(
            query=f"""CREATE TABLE local_src AS
                      SELECT i AS "ID", 'row-' || i AS "NAME"
                      FROM range(1, {rows + 1}) t(i)"""
        )

        # (1) Write through the attached catalog with plain SQL — no function.
        con.execute(query="USE db2.DB2INST1")
        con.execute(query=f"CREATE TABLE {ctas_table} AS SELECT * FROM memory.local_src")
        con.execute(query="USE memory")

        # Read it back through the attached catalog (first col is INT, so
        # COUNT(*) is safe here).
        (n_attached,) = con.execute(
            query=f"SELECT COUNT(*) FROM db2.DB2INST1.{ctas_table} WHERE ID > {rows - 100}"
        ).fetchone()
        assert n_attached == 100

        # (2) Write through the adbc_insert function.
        con.execute(query="SET VARIABLE db2 = adbc_connect({'secret': 'db2_secret'})")
        (inserted,) = con.execute(
            query=f"""SELECT * FROM adbc_insert(getvariable('db2')::BIGINT, '{insert_table}',
                         (SELECT * FROM local_src), mode := 'create')"""
        ).fetchone()
        assert inserted == rows

        con.execute(query="DETACH db2")

        # Verify both landed, independently, through the plain Python DBAPI.
        with db2.connect(
            uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
        ) as c, c.cursor() as cur:
            for name in (ctas_table, insert_table):
                cur.execute(f"SELECT COUNT(*), SUM(ID) FROM {name}")
                count, total = cur.fetchone()
                assert count == rows, name
                assert total == rows * (rows + 1) // 2, name

        con.execute(query="SELECT adbc_disconnect(getvariable('db2')::BIGINT)")
    finally:
        con.close()
        drop(ctas_table, insert_table)

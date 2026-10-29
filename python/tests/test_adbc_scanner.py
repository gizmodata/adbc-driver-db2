"""Tier-3 integration test: Hardcharger (https://gizmodata.com/hardcharger)
and embedded DuckDB reading from and writing to Db2 *live* through the
``adbc_scanner`` extension, loading this repo's c-shared driver library:

    Hardcharger / duckdb  ──adbc_scanner──▶  libadbc_driver_db2  ──DRDA──▶  Db2

Every test runs twice: inside a Hardcharger server (the SQL goes over Chargewire
and the server loads the driver) and in an in-process DuckDB. On DuckDB 2.0
and later (every Hardcharger) the extension comes from the Query.Farm
repository; DuckDB 1.x installs it from community.
"""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.duckdb_ext]


def lit(value: str) -> str:
    """A SQL string literal (the SQL travels as text, so no bind parameters)."""
    return "'" + value.replace("'", "''") + "'"


@dataclass
class Engine:
    """Somewhere to run DuckDB SQL: an in-process DuckDB or a Hardcharger session."""

    name: str
    run: Callable[[str], list[tuple]]
    catalog: str  # the engine's own default catalog, for USE after a write into Db2

    def one(self, sql: str) -> tuple:
        (row,) = self.run(sql)
        return row


def install_adbc_scanner(run: Callable[[str], list[tuple]]) -> None:
    ((version,),) = run("SELECT version()")
    if int(version.lstrip("v").split(".")[0]) >= 2:
        run("SET allow_extension_repositories = 'allowed'")
        run(
            "CREATE EXTENSION REPOSITORY IF NOT EXISTS query_farm "
            "WITH PREFIX 'https://duckdb-extensions.query.farm'"
        )
        run("INSTALL adbc_scanner FROM query_farm")
        run("LOAD adbc_scanner FROM query_farm")
    else:
        run("INSTALL adbc_scanner FROM community")
        run("LOAD adbc_scanner")


def db2_execute(db2_server, *statements: str, ignore_errors: bool = False) -> None:
    import adbc_driver_db2.dbapi as db2

    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
    ) as conn, conn.cursor() as cur:
        for sql in statements:
            try:
                cur.execute(operation=sql)
            except Exception:
                if not ignore_errors:
                    raise


@pytest.fixture(
    params=[
        pytest.param("hardcharger", marks=pytest.mark.hardcharger),
        pytest.param("duckdb"),
    ]
)
def engine(request, db2_server, driver_path):
    if request.param == "hardcharger":
        server = request.getfixturevalue("hardcharger_server")
        conn = server.connect(autocommit=True)
        cur = conn.cursor()

        def run(sql: str) -> list[tuple]:
            cur.execute(operation=sql)
            return cur.fetchall() if cur.description else []

        close = conn.close
        library = server.driver_path or driver_path
    else:
        duckdb = pytest.importorskip("duckdb")
        con = duckdb.connect(database=":memory:")

        def run(sql: str) -> list[tuple]:
            return con.execute(query=sql).fetchall()

        close = con.close
        library = driver_path

    try:
        install_adbc_scanner(run=run)
        ((catalog,),) = run("SELECT current_database()")
        # Credentials live in a secret (README "Query Db2 live from Hardcharger or
        # DuckDB"); ATTACH exposes Db2 as a catalog, and the function API
        # (adbc_scan / adbc_insert) takes the attached database's name.
        run(
            f"""CREATE OR REPLACE SECRET db2_secret (
                TYPE adbc,
                SCOPE {lit(db2_server.uri)},
                driver {lit(library)},
                entrypoint 'Db2DriverInit',
                uri {lit(db2_server.uri)},
                username {lit(db2_server.user)},
                password {lit(db2_server.password)}
            )"""
        )
        run("DETACH DATABASE IF EXISTS db2")
        run(f"ATTACH {lit(db2_server.uri)} AS db2 (TYPE adbc)")
    except Exception:
        close()
        raise
    yield Engine(name=request.param, run=run, catalog=catalog)
    try:
        run(f'USE "{catalog}"')
        run("DETACH db2")
        run("DROP SECRET db2_secret")
    finally:
        close()


def test_adbc_scan_pulls_from_db2(engine, db2_server, table_name):
    # A small user table to scan (SYSCAT tables are wide and slow to describe).
    db2_execute(
        db2_server,
        f"CREATE TABLE {table_name} (ID INTEGER NOT NULL, NAME VARCHAR(20))",
        f"INSERT INTO {table_name} SELECT N, 'n' || CAST(N AS VARCHAR(5)) FROM (VALUES 1,2,3,4,5,6,7,8,9) AS T(N)",
    )
    try:
        # Pull through the storage extension. Two things to know about Db2's
        # catalog: schema names are stored blank-padded ('SYSCAT  '), which
        # Db2 ignores in comparisons but DuckDB does not, hence TRIM; and
        # COUNT(*) on a table whose first column is a string trips an
        # adbc_scanner bug, hence COUNT(TABNAME).
        n, first_schema = engine.one(
            "SELECT COUNT(TABNAME), MIN(TABSCHEMA) FROM db2.SYSCAT.TABLES WHERE TRIM(TABSCHEMA) = 'SYSCAT'"
        )
        assert n > 100
        assert first_schema.strip() == "SYSCAT"

        # Projection + filter pushdown against a user table (int-first, so
        # COUNT(*) is fine here).
        (n,) = engine.one(f"SELECT COUNT(*) FROM db2.DB2INST1.{table_name} WHERE ID > 5")
        assert n == 4

        # Types survive the trip: Db2 DECIMAL/TIMESTAMP become DuckDB DECIMAL/TIMESTAMP.
        kinds = {name: typ for name, typ, *_ in engine.run("DESCRIBE db2.SYSCAT.TABLES")}
        assert kinds["TABSCHEMA"] == "VARCHAR"
        assert kinds["CREATE_TIME"].startswith("TIMESTAMP")
        assert kinds["CARD"] == "BIGINT"

        # The same attached database drives the function API for arbitrary Db2 SQL.
        typed = engine.run(
            """DESCRIBE SELECT * FROM adbc_scan('db2',
               'SELECT CAST(1.25 AS DECIMAL(10,2)) AS D, CURRENT TIMESTAMP AS TS FROM SYSIBM.SYSDUMMY1')"""
        )
        fkinds = {name: typ for name, typ, *_ in typed}
        assert fkinds["D"].startswith("DECIMAL")
        assert fkinds["TS"].startswith("TIMESTAMP")
    finally:
        db2_execute(db2_server, f"DROP TABLE {table_name}", ignore_errors=True)


def test_writes_to_db2(engine, db2_server, table_name):
    """Writes back into Db2 through adbc_scanner two ways:

    ``USE db2.<schema>; CREATE TABLE ... AS SELECT`` through the attached
    catalog (plain SQL, no function call), and the ``adbc_insert`` function.
    Both bulk-ingest a relation into a brand-new Db2 table.
    """
    import adbc_driver_db2.dbapi as db2

    ctas_table = f"{table_name}_CTAS"
    insert_table = f"{table_name}_INS"
    local = f"local_{table_name.lower()}"  # a Hardcharger server is shared: no fixed names

    rows = 2_000
    # Quote the aliases uppercase so the columns land as Db2's idiomatic
    # ID/NAME (adbc_scanner preserves the column case into Db2, and DuckDB
    # lowercases unquoted identifiers).
    engine.run(
        f"""CREATE TABLE "{engine.catalog}".main.{local} AS
            SELECT i AS "ID", 'row-' || i AS "NAME" FROM range(1, {rows + 1}) t(i)"""
    )
    try:
        # (1) Write through the attached catalog with plain SQL — no function.
        engine.run("USE db2.DB2INST1")
        engine.run(f'CREATE TABLE {ctas_table} AS SELECT * FROM "{engine.catalog}".main.{local}')
        engine.run(f'USE "{engine.catalog}"')

        # Read it back through the attached catalog (first col is INT, so
        # COUNT(*) is safe here).
        (n_attached,) = engine.one(f"SELECT COUNT(*) FROM db2.DB2INST1.{ctas_table} WHERE ID > {rows - 100}")
        assert n_attached == 100

        # (2) Write through the adbc_insert function.
        (inserted,) = engine.one(
            f"""SELECT * FROM adbc_insert('db2', {lit(insert_table)},
                    (SELECT * FROM "{engine.catalog}".main.{local}), mode := 'create')"""
        )
        assert inserted == rows

        # Verify both landed, independently, through the plain Python DBAPI.
        with db2.connect(
            uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
        ) as c, c.cursor() as cur:
            for name in (ctas_table, insert_table):
                cur.execute(operation=f"SELECT COUNT(*), SUM(ID) FROM {name}")
                count, total = cur.fetchone()
                assert count == rows, name
                assert total == rows * (rows + 1) // 2, name
    finally:
        engine.run(f'USE "{engine.catalog}"')
        engine.run(f'DROP TABLE IF EXISTS "{engine.catalog}".main.{local}')
        db2_execute(db2_server, f"DROP TABLE {ctas_table}", f"DROP TABLE {insert_table}", ignore_errors=True)

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

    con = duckdb.connect(database=":memory:")
    try:
        con.execute(query="INSTALL adbc_scanner FROM community")
        con.execute(query="LOAD adbc_scanner")
    except Exception as exc:  # pragma: no cover - environment dependent
        pytest.skip(reason=f"adbc_scanner extension unavailable: {exc}")

    con.execute(
        query="""SET VARIABLE db2 = adbc_connect({
            'driver': $driver,
            'entrypoint': 'Db2DriverInit',
            'uri': $uri,
            'username': $username,
            'password': $password
        })""",
        parameters={
            "driver": adbc_driver_db2._driver_path(),
            "uri": db2_server.uri,
            "username": db2_server.user,
            "password": db2_server.password,
        },
    )

    # Pull: a Db2 result set materialised inside DuckDB.
    rows = con.execute(
        query="""SELECT COUNT(*) AS n, MIN(TABSCHEMA) AS first_schema
                 FROM adbc_scan(getvariable('db2')::BIGINT,
                                'SELECT TABSCHEMA, TABNAME FROM SYSCAT.TABLES')"""
    ).fetchone()
    assert rows[0] > 100
    assert rows[1].strip() != ""

    # Types survive the trip: Db2 DECIMAL/TIMESTAMP become DuckDB DECIMAL/TIMESTAMP.
    typed = con.execute(
        query="""DESCRIBE SELECT * FROM adbc_scan(getvariable('db2')::BIGINT,
                 'SELECT CAST(1.25 AS DECIMAL(10,2)) AS D, CURRENT TIMESTAMP AS TS, CAST(''x'' AS VARCHAR(5)) AS S FROM SYSIBM.SYSDUMMY1')"""
    ).fetchall()
    kinds = {name: typ for name, typ, *_ in typed}
    assert kinds["D"].startswith("DECIMAL")
    assert kinds["TS"].startswith("TIMESTAMP")
    assert kinds["S"] == "VARCHAR"

    con.execute(query="SELECT adbc_disconnect(getvariable('db2')::BIGINT)")
    con.close()

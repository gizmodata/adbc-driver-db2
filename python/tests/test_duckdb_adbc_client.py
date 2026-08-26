"""Tier-3 integration test #2: DuckDB's ``adbc`` community extension (by
Columnar, https://github.com/columnar-tech/duckdb-adbc-client) driving
this driver through an ADBC *connection profile*:

    duckdb ──adbc (profile://)──▶ libadbc_driver_db2 ──DRDA──▶ Db2

Exercises the driver manifest + profile resolution, ``read_adbc``,
``ATTACH`` catalog browsing, and the extension's INSERT / CTAS path,
which uses ADBC bulk ingest (i.e. this driver's ingest implementation).
"""

from __future__ import annotations

import os
import textwrap

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.duckdb_ext]


@pytest.fixture()
def profile_env(tmp_path, db2_server, monkeypatch):
    """Install the driver manifest and a connection profile into temp dirs
    that the ADBC driver manager searches via environment variables."""
    import adbc_driver_db2

    drivers = tmp_path / "drivers"
    profiles = tmp_path / "profiles"
    profiles.mkdir()
    adbc_driver_db2.install_manifest(directory=drivers)  # writes db2.toml
    (profiles / "db2test.toml").write_text(
        textwrap.dedent(
            f"""\
            profile_version = 1
            driver = "db2"

            [Options]
            uri = "{db2_server.uri}"
            username = "{db2_server.user}"
            password = "{db2_server.password}"
            """
        ),
        encoding="utf-8",
    )
    monkeypatch.setenv("ADBC_DRIVER_PATH", str(drivers))
    monkeypatch.setenv("ADBC_PROFILE_PATH", str(profiles))
    return "profile://db2test"


def test_duckdb_adbc_extension_read_attach_and_write(profile_env, db2_server):
    duckdb = pytest.importorskip("duckdb")
    import adbc_driver_db2.dbapi as db2

    con = duckdb.connect(database=":memory:")
    try:
        con.execute(query="INSTALL adbc FROM community")
        con.execute(query="LOAD adbc")
    except Exception as exc:  # pragma: no cover - environment dependent
        pytest.skip(reason=f"adbc extension unavailable: {exc}")

    # read_adbc: arbitrary Db2 SQL through the profile.
    (n,) = con.execute(
        query="SELECT COUNT(*) FROM read_adbc($p, 'SELECT TABSCHEMA, TABNAME FROM SYSCAT.TABLES')",
        parameters={"p": profile_env},
    ).fetchone()
    assert n > 100

    # ATTACH: Db2 as a catalog; write through CTAS + INSERT (bulk ingest).
    con.execute(query=f"ATTACH '{profile_env}' AS db2 (TYPE adbc)")
    schema = db2_server.user.upper()
    con.execute(query=f'USE db2."{schema}"')
    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
    ) as c, c.cursor() as cur:
        try:
            cur.execute("DROP TABLE ADBC_EXT_T")
        except Exception:
            pass
    con.execute(
        query="""CREATE TABLE ADBC_EXT_T AS
                 SELECT i::INTEGER AS ID, 'row-' || i AS NAME, (i * 0.25)::DECIMAL(10,2) AS AMT
                 FROM range(1, 1001) t(i)"""
    )
    con.execute(query="INSERT INTO ADBC_EXT_T SELECT 1001, 'extra', 0.5")
    (count, total) = con.execute(query="SELECT COUNT(*), SUM(ID) FROM ADBC_EXT_T").fetchone()
    assert count == 1001
    assert total == 1001 * 1002 // 2

    # And through the plain DBAPI, so the data really landed in Db2.
    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
    ) as c, c.cursor() as cur:
        cur.execute('SELECT COUNT(*), MAX("NAME"), SUM("AMT") FROM ADBC_EXT_T')
        cnt, max_name, amt = cur.fetchone()
        assert cnt == 1001
        assert max_name == "row-999"
        assert float(amt) == 1000 * 1001 // 2 * 0.25 + 0.5
        cur.execute("DROP TABLE ADBC_EXT_T")
    con.execute(query="USE memory")
    con.execute(query="DETACH db2")
    con.close()

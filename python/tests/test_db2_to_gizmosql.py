"""Db2 -> GizmoSQL streaming ingest: the customer-notebook flow, ADBC to ADBC.

Pulls a result set out of Db2 as an Arrow RecordBatchReader (one query
block at a time, so client memory stays bounded) and hands the reader
straight to GizmoSQL's ``adbc_ingest`` — no pandas, no ODBC.

Needs both a Db2 (DB2_HOST ...) and a GizmoSQL (GIZMOSQL_URI plus
GIZMOSQL_TOKEN or GIZMOSQL_USERNAME/GIZMOSQL_PASSWORD).
"""

from __future__ import annotations

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.gizmosql]


def test_stream_db2_table_into_gizmosql(db2_server, gizmosql_server):
    import adbc_driver_db2.dbapi as db2

    gizmosql = pytest.importorskip("adbc_driver_gizmosql.dbapi")

    rows = 250_000
    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
    ) as src, src.cursor() as src_cur:
        try:
            src_cur.execute("DROP TABLE ADBC_TO_GIZMO")
        except Exception:
            pass
        src_cur.execute(
            "CREATE TABLE ADBC_TO_GIZMO (ID INTEGER NOT NULL, NAME VARCHAR(40), AMT DECIMAL(12,2), TS TIMESTAMP)"
        )
        src_cur.execute(
            f"""INSERT INTO ADBC_TO_GIZMO
                WITH T(N) AS (VALUES 1 UNION ALL SELECT N + 1 FROM T WHERE N < {rows})
                SELECT N, 'name-' || CAST(N AS VARCHAR(10)), N * 0.25, CURRENT TIMESTAMP FROM T"""
        )

        # Exactly the notebook's shape: SELECT from Db2, ingest into GizmoSQL.
        src_cur.execute("SELECT * FROM ADBC_TO_GIZMO")
        reader = src_cur.fetch_record_batch()  # streams; ~65k rows per batch

        with gizmosql.connect(
            gizmosql_server.uri, username=gizmosql_server.username, password=gizmosql_server.password
        ) as dst, dst.cursor() as dst_cur:
            loaded = dst_cur.adbc_ingest("adbc_to_gizmo", reader, mode="replace")
            dst.commit()
            assert loaded == rows
            dst_cur.execute("SELECT COUNT(*), SUM(ID), MAX(NAME) FROM adbc_to_gizmo")
            count, total, max_name = dst_cur.fetchone()
            assert count == rows
            assert total == rows * (rows + 1) // 2
            assert max_name == "name-99999"
            dst_cur.execute("DROP TABLE adbc_to_gizmo")

        src_cur.execute("DROP TABLE ADBC_TO_GIZMO")

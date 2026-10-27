"""Db2 -> Hardcharger streaming ingest: the customer-notebook flow, ADBC to ADBC.

Pulls a result set out of Db2 as an Arrow RecordBatchReader (one query
block at a time, so client memory stays bounded) and hands the reader
straight to Hardcharger's ``adbc_ingest`` — no pandas, no ODBC.

Needs both a Db2 (DB2_HOST ...) and a Hardcharger (see the
``hardcharger_server`` fixture). Hardcharger is a *test-only* dependency:
the driver itself has no Hardcharger code.
"""

from __future__ import annotations

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.hardcharger]


def test_stream_db2_table_into_hardcharger(db2_server, hardcharger_server, table_name):
    import adbc_driver_db2.dbapi as db2

    rows = 250_000
    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password, autocommit=True
    ) as src, src.cursor() as src_cur:
        src_cur.execute(
            operation=f"CREATE TABLE {table_name} (ID INTEGER NOT NULL, NAME VARCHAR(40), AMT DECIMAL(12,2), TS TIMESTAMP)"
        )
        try:
            src_cur.execute(
                operation=f"""INSERT INTO {table_name}
                    WITH T(N) AS (VALUES 1 UNION ALL SELECT N + 1 FROM T WHERE N < {rows})
                    SELECT N, 'name-' || CAST(N AS VARCHAR(10)), N * 0.25, CURRENT TIMESTAMP FROM T"""
            )

            # Exactly the notebook's shape: SELECT from Db2, ingest into Hardcharger.
            src_cur.execute(operation=f"SELECT * FROM {table_name}")
            reader = src_cur.fetch_record_batch()  # streams; ~65k rows per batch

            dest = table_name.lower()
            with hardcharger_server.connect() as dst, dst.cursor() as dst_cur:
                loaded = dst_cur.adbc_ingest(table_name=dest, data=reader, mode="replace")
                dst.commit()
                assert loaded == rows
                # Hardcharger ingests in parallel parts, so the rows land in no defined
                # order: check aggregates, not positions.
                dst_cur.execute(operation=f"SELECT COUNT(*), SUM(ID), MAX(NAME), SUM(AMT) FROM {dest}")
                count, total, max_name, amt_sum = dst_cur.fetchone()
                assert count == rows
                assert total == rows * (rows + 1) // 2
                assert max_name == "name-99999"
                assert float(amt_sum) == rows * (rows + 1) // 2 * 0.25
                dst_cur.execute(operation=f"DROP TABLE {dest}")
                dst.commit()
        finally:
            src_cur.execute(operation=f"DROP TABLE {table_name}")

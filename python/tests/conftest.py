"""
pytest fixtures for the adbc-driver-db2 integration tests.

The tests need a live Db2. Point them at one with environment
variables (defaults match the icr.io/db2_community/db2 container used
in CI, started as
``docker run -e LICENSE=accept -e DB2INST1_PASSWORD=password -e DBNAME=testdb -p 50000:50000 icr.io/db2_community/db2``):

    DB2_HOST      (required — tests are skipped when unset)
    DB2_PORT      (default 50000)
    DB2_DATABASE  (default testdb)
    DB2_USER      (default db2inst1)
    DB2_PASSWORD  (default password)

The Db2 -> GizmoSQL test additionally needs GIZMOSQL_URI (and
GIZMOSQL_TOKEN or GIZMOSQL_USERNAME / GIZMOSQL_PASSWORD).
"""

from __future__ import annotations

import os
from dataclasses import dataclass

import pytest


@dataclass
class Db2Server:
    host: str
    port: int
    database: str
    user: str
    password: str

    @property
    def uri(self) -> str:
        return f"db2://{self.host}:{self.port}/{self.database}"

    @property
    def uri_with_credentials(self) -> str:
        return f"db2://{self.user}:{self.password}@{self.host}:{self.port}/{self.database}"


@pytest.fixture(scope="session")
def db2_server() -> Db2Server:
    host = os.environ.get("DB2_HOST")
    if not host:
        pytest.skip(reason="DB2_HOST not set")
    return Db2Server(
        host=host,
        port=int(os.environ.get("DB2_PORT", "50000")),
        database=os.environ.get("DB2_DATABASE", "testdb"),
        user=os.environ.get("DB2_USER", "db2inst1"),
        password=os.environ.get("DB2_PASSWORD", "password"),
    )


@dataclass
class GizmoSQLServer:
    uri: str
    username: str
    password: str


@pytest.fixture(scope="session")
def gizmosql_server() -> GizmoSQLServer:
    uri = os.environ.get("GIZMOSQL_URI")
    if not uri:
        pytest.skip(reason="GIZMOSQL_URI not set")
    token = os.environ.get("GIZMOSQL_TOKEN")
    if token:
        return GizmoSQLServer(uri=uri, username="token", password=token)
    return GizmoSQLServer(
        uri=uri,
        username=os.environ.get("GIZMOSQL_USERNAME", "gizmosql_user"),
        password=os.environ.get("GIZMOSQL_PASSWORD", "gizmosql_password"),
    )

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

The Db2 -> Hardcharger tests additionally need a Hardcharger server
(https://gizmodata.com/hardcharger): the one named by
ADBC_DB2_TEST_HARDCHARGER_URI (CI's container), or else one started from the
``hardcharger`` PyPI package (``pip install -e ".[test,hardcharger]"``). A
server named by that variable must answer, or the tests fail: they never skip
because a configured server is down.
"""

from __future__ import annotations

import os
import time
import uuid
from dataclasses import dataclass
from pathlib import Path

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


@pytest.fixture(scope="session")
def driver_path() -> str:
    """The Db2 ADBC driver library this test run uses."""
    candidate = os.environ.get("ADBC_DB2_LIBRARY")
    if candidate and Path(candidate).is_file():
        return candidate
    from adbc_driver_db2 import _driver_path

    return _driver_path()


@pytest.fixture()
def table_name() -> str:
    """A unique, upper-case test table name."""
    return f"ADBC_TEST_{uuid.uuid4().hex[:12].upper()}"


HARDCHARGER_MIN_VERSION = (2, 1)


@dataclass
class HardchargerServer:
    """Connection details for a Hardcharger server (test-only dependency)."""

    uri: str
    username: str
    password: str
    # The Db2 driver library as the server's own file system sees it (a bind
    # mount in CI's container); None: the local build, for a local server.
    driver_path: str | None = None

    def connect(self, autocommit: bool = False):
        from adbc_driver_hardcharger import dbapi as hardcharger

        return hardcharger.connect(
            uri=self.uri, username=self.username, password=self.password, autocommit=autocommit
        )


def _wait_for_hardcharger(server: HardchargerServer, timeout: float = 120.0) -> None:
    deadline = time.monotonic() + timeout
    last: Exception | None = None
    while time.monotonic() < deadline:
        try:
            with server.connect() as conn, conn.cursor() as cur:
                cur.execute(operation="SELECT HARDCHARGER_VERSION()")
                (version,) = cur.fetchone()
        except Exception as exc:  # not up yet
            last = exc
            time.sleep(2)
            continue
        major_minor = tuple(int(part) for part in version.split(".")[:2])
        if major_minor < HARDCHARGER_MIN_VERSION:
            pytest.fail(reason=f"Hardcharger at {server.uri} is {version}; these tests need 2.1 or later")
        return
    pytest.fail(reason=f"no Hardcharger answering at {server.uri} after {timeout:.0f}s: {last}")


@pytest.fixture(scope="session")
def hardcharger_server():
    uri = os.environ.get("ADBC_DB2_TEST_HARDCHARGER_URI")
    if uri:
        server = HardchargerServer(
            uri=uri,
            username=os.environ.get("ADBC_DB2_TEST_HARDCHARGER_USERNAME", "hardcharger"),
            password=os.environ.get("ADBC_DB2_TEST_HARDCHARGER_PASSWORD", "hardcharger"),
            driver_path=os.environ.get("ADBC_DB2_TEST_HARDCHARGER_DRIVER_PATH"),
        )
        _wait_for_hardcharger(server=server)
        yield server
        return
    hardcharger = pytest.importorskip(
        "hardcharger", reason="no ADBC_DB2_TEST_HARDCHARGER_URI and the hardcharger package isn't installed"
    )
    pytest.importorskip("adbc_driver_hardcharger")
    with hardcharger.Server() as local:
        server = HardchargerServer(uri=local.uri, username=local.username, password=local.password)
        _wait_for_hardcharger(server=server)
        yield server

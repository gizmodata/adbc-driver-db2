# Copyright (c) 2025-2026 ADBC Drivers Contributors
# Copyright (c) 2026 GizmoData LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#         http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""pytest configuration for running the adbc-drivers.org suite against Db2.

The suite needs a live Db2 server, named by ADBC_DB2_TEST_URI, e.g.
``db2://db2inst1:password@127.0.0.1:50000/testdb``. It is a release gate, so
it never skips for lack of a server: an unset variable or an unreachable
server fails the session at startup, and so does a run in which no test
passed.
"""

import os
import sys
from pathlib import Path

import adbc_driver_manager.dbapi
import adbc_drivers_validation.model
import adbc_drivers_validation.tests.conftest
import pytest
from adbc_drivers_validation.tests.conftest import (  # noqa: F401
    conn,
    conn_factory,
    db_kwargs,
    manual_test,
    noci,
    pytest_collection_modifyitems,
)

from .db2 import URI_ENV, get_quirks

DRIVER_PATH_ENV = "ADBC_DB2_TEST_DRIVER_PATH"


def pytest_addoption(parser: pytest.Parser) -> None:
    adbc_drivers_validation.tests.conftest.pytest_addoption(parser)
    parser.addoption("--vendor-version", action="store", default="12.1")


def _driver_path() -> str:
    if path := os.environ.get(DRIVER_PATH_ENV):
        return path
    ext = {"win32": "dll", "darwin": "dylib"}.get(sys.platform, "so")
    return str(Path(__file__).parents[2] / "pkg" / "db2" / f"libadbc_driver_db2.{ext}")


def pytest_sessionstart(session: pytest.Session) -> None:
    """Fail (never skip) when the driver or the server is missing."""
    if session.config.option.collectonly:
        return
    path = _driver_path()
    if not Path(path).is_file():
        pytest.exit(
            f"driver library {path} not found (build it, or set {DRIVER_PATH_ENV})",
            returncode=1,
        )
    uri = os.environ.get(URI_ENV)
    if not uri:
        pytest.exit(f"{URI_ENV} must name a Db2 server", returncode=1)
    try:
        with (
            adbc_driver_manager.dbapi.connect(
                driver=path, db_kwargs={"uri": uri}, autocommit=True
            ) as connection,
            connection.cursor() as cursor,
        ):
            cursor.execute("SELECT 1 FROM SYSIBM.SYSDUMMY1")
            cursor.fetchall()
            # The suite's secondary schema (ingest into / read from a schema
            # other than the current one).
            quirks = get_quirks(session.config.getoption("vendor_version"))
            cursor.execute(
                "SELECT 1 FROM SYSCAT.SCHEMATA WHERE SCHEMANAME = ?",
                parameters=(quirks.features.secondary_schema,),
            )
            if not cursor.fetchall():
                cursor.execute(f'CREATE SCHEMA "{quirks.features.secondary_schema}"')
            # Declared global temporary tables (temporary ingest) need a
            # USER TEMPORARY tablespace, which a fresh Db2 doesn't have.
            cursor.execute(
                "SELECT 1 FROM SYSCAT.TABLESPACES WHERE DATATYPE = 'U'"
            )
            if not cursor.fetchall():
                cursor.execute(
                    "CREATE USER TEMPORARY TABLESPACE ADBC_USERTEMP"
                    " MANAGED BY AUTOMATIC STORAGE"
                )
    except Exception as e:  # noqa: BLE001
        pytest.exit(f"cannot reach the Db2 server in {URI_ENV}: {e}", returncode=1)


def pytest_sessionfinish(session: pytest.Session, exitstatus: int) -> None:
    """A gate run in which nothing passed is a failure, not a green build."""
    if session.config.option.collectonly or exitstatus != 0:
        return
    reporter = session.config.pluginmanager.get_plugin("terminalreporter")
    if reporter is not None and not reporter.stats.get("passed"):
        session.exitstatus = 1


@pytest.fixture(scope="session")
def driver(
    request: pytest.FixtureRequest, pytestconfig: pytest.Config
) -> adbc_drivers_validation.model.DriverQuirks:
    assert request.param.startswith("db2")
    return get_quirks(pytestconfig.getoption("vendor_version"))


@pytest.fixture(scope="session")
def driver_path(driver: adbc_drivers_validation.model.DriverQuirks) -> str:
    return _driver_path()

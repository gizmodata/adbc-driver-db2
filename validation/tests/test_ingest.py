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

import adbc_drivers_validation.tests.ingest as ingest_tests
import pytest

from .db2 import get_quirks


def pytest_generate_tests(metafunc) -> None:
    quirks = [get_quirks(metafunc.config.getoption("vendor_version"))]
    return ingest_tests.generate_tests(quirks, metafunc)


class TestIngest(ingest_tests.TestIngest):
    @pytest.mark.xfail(
        reason="Db2's catalog (SYSCAT.TABLES) does not list declared global temporary"
        " tables; only the privileged ADMIN_GET_TEMP_TABLES routine sees them",
        strict=True,
    )
    def test_temporary_get_objects(self, driver, conn_factory, query) -> None:
        super().test_temporary_get_objects(driver, conn_factory, query)

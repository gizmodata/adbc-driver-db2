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

"""Db2 quirks for the adbc-drivers.org validation suite.

Every deviation from the suite's defaults is listed here or in a query override
under ../queries, each with the reason. A feature is declared unsupported only
when Db2 itself (or ADBC's semantics for it) lacks it, never to hide a driver
bug.
"""

import functools
import re
from pathlib import Path

from adbc_drivers_validation import model

URI_ENV = "ADBC_DB2_TEST_URI"


class Db2Quirks(model.DriverQuirks):
    name = "db2"
    driver = "adbc_driver_db2"
    driver_name = "ADBC Db2 Driver - Go"
    vendor_name = "IBM Db2"
    # Db2 reports its product id (PRDID) as the version, SQLvvrrm: SQL12015 is
    # 12.1 mod pack 5 (JDBC's getDatabaseProductVersion says the same).
    vendor_version = re.compile(r"SQL1201[0-9]+")
    short_version = "12.1"
    features = model.DriverFeatures(
        connection_get_table_schema=True,
        connection_set_current_catalog=False,
        connection_set_current_schema=True,
        connection_transactions=True,
        get_objects=True,
        get_objects_constraints_check=True,
        get_objects_constraints_foreign=True,
        get_objects_constraints_primary=True,
        get_objects_constraints_unique=True,
        get_objects_tabletypes_filter=True,
        statement_bind=True,
        statement_bulk_ingest=True,
        statement_bulk_ingest_schema=True,
        statement_bulk_ingest_temporary=True,
        statement_execute_schema=True,
        statement_get_parameter_schema=True,
        statement_prepare=True,
        statement_rows_affected=True,
        statement_rows_affected_ddl=False,
        current_catalog="TESTDB",
        current_schema="DB2INST1",
        secondary_schema="ADBC_VALIDATION_SECONDARY",
        supported_xdbc_fields=["xdbc_nullable", "xdbc_is_nullable"],
    )
    setup = model.DriverSetup(
        database={"uri": model.FromEnv(URI_ENV)},
        connection={},
        statement={},
    )

    @property
    def queries_paths(self) -> tuple[Path]:
        return (Path(__file__).parent.parent / "queries",)

    @property
    def sample_ddl_constraints(self) -> list[str]:
        # Identifiers are quoted to keep them lower case (Db2 folds unquoted
        # names to upper case). Key columns must be NOT NULL in Db2. The
        # leading "z" column keeps the constraint columns off ordinal
        # position 1 (Db2 can't drop it without a REORG, so it stays).
        return [
            'CREATE TABLE "constraint_check" ("z" INT, "a" INT, "b" INT, CHECK ("a" > 0), CHECK ("a" + "b" > 0))',
            'CREATE TABLE "constraint_primary" ("z" INT, "a" INT NOT NULL, "b" VARCHAR(100), PRIMARY KEY ("a"))',
            'CREATE TABLE "constraint_primary_multi" ("z" INT, "a" INT NOT NULL, "b" VARCHAR(100) NOT NULL, PRIMARY KEY ("b", "a"))',
            'CREATE TABLE "constraint_primary_multi2" ("z" INT, "a" VARCHAR(100) NOT NULL, "b" INT NOT NULL, PRIMARY KEY ("a", "b"))',
            'CREATE TABLE "constraint_unique" ("z" INT, "a" INT NOT NULL, "b" INT NOT NULL, "c" INT NOT NULL, UNIQUE ("a"), UNIQUE ("c", "b"))',
            'CREATE TABLE "constraint_foreign" ("z" INT, "a" INT, "b" INT, FOREIGN KEY ("b") REFERENCES "constraint_primary" ("a"))',
            'CREATE TABLE "constraint_foreign_multi" ("z" INT, "a" INT, "b" INT, "c" VARCHAR(100), FOREIGN KEY ("c", "b") REFERENCES "constraint_primary_multi2" ("a", "b"))',
        ]

    def query_override(self, context: str, default: str) -> str:
        # Db2 needs a length on VARCHAR, and folds unquoted identifiers to
        # upper case, so the suite's lower-case column names are quoted.
        if context == "TestStatement.sample_table":
            return default.replace("id INT, value VARCHAR", '"id" INT, "value" VARCHAR(100)')
        if context == "TestConnection.test_get_table_schema_schema":
            return default.replace("eggs VARCHAR", "eggs VARCHAR(100)")
        if context == "TestStatement.test_execute_schema_noalias":
            return default.replace("SELECT id + 1", 'SELECT "id" + 1')
        return super().query_override(context, default)

    def is_table_not_found(self, table_name: str | None, error: Exception) -> bool:
        # SQL0204N: "<name>" is an undefined name (SQLSTATE 42704).
        text = str(error)
        if "SQLCODE=-204" not in text and "42704" not in text:
            return False
        return table_name is None or table_name.upper() in text.upper()

    def split_statement(self, statement: str) -> list[str]:
        """Split a script on semicolons outside quotes, dropping -- comments.

        Db2 runs one statement at a time. The suite's line-based fallback
        keeps a statement that ends in "; -- comment" glued to the next one,
        and sqlglot has no Db2 dialect to parse with.
        """
        statements: list[str] = []
        current: list[str] = []
        quote: str | None = None
        i = 0
        while i < len(statement):
            ch = statement[i]
            if quote:
                current.append(ch)
                if ch == quote:
                    if statement[i + 1 : i + 2] == quote:  # doubled quote
                        current.append(ch)
                        i += 1
                    else:
                        quote = None
            elif ch in "'\"":
                quote = ch
                current.append(ch)
            elif statement.startswith("--", i):
                while i < len(statement) and statement[i] != "\n":
                    i += 1
                continue
            elif ch == ";":
                if text := "".join(current).strip():
                    statements.append(text)
                current = []
            else:
                current.append(ch)
            i += 1
        if text := "".join(current).strip():
            statements.append(text)
        return statements

    def qualify_temp_table(self, cursor, name: str) -> list[str]:
        # Declared global temporary tables always live in the SESSION schema.
        return ["SESSION", name]


@functools.cache
def get_quirks(version: str) -> Db2Quirks:
    db2_quirks = Db2Quirks()
    if version != db2_quirks.short_version:
        raise ValueError(f"Unsupported Db2 version: {version}")
    return db2_quirks

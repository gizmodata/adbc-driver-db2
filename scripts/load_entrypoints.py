"""Load the installed adbc-driver-db2 library through each entrypoint.

Run after installing a wheel (CI: the wheel jobs and the old-glibc job). No
Db2 server is needed: each load must get as far as connecting, which fails
with a refused connection on 127.0.0.1:1. A library that can't load (a missing
GLIBC_x.y symbol version, wrong architecture) or a missing entrypoint fails
before that, with a different error. A driver-internal name that 0.2.4 still
exported (Db2DatabaseNew) must no longer be found.

    python -I scripts/load_entrypoints.py
"""

from __future__ import annotations

import adbc_driver_manager
import adbc_driver_manager.dbapi

import adbc_driver_db2

URI = "db2://nobody:nothing@127.0.0.1:1/NODB"
LOAD_FAILURES = ("GLIBC", "dlopen", "Could not load", "could not load", "image not found")


def connect_refused(entrypoint: str | None) -> None:
    kwargs = {} if entrypoint is None else {"entrypoint": entrypoint}
    try:
        adbc_driver_manager.dbapi.connect(
            driver=adbc_driver_db2._driver_path(), db_kwargs={"uri": URI}, **kwargs
        ).close()
    except Exception as e:  # noqa: BLE001
        message = str(e)
        print(f"entrypoint={entrypoint!r}: {type(e).__name__}: {message[:200]}")
        assert not any(s in message for s in LOAD_FAILURES), message
        assert "refused" in message.lower() or "connect" in message.lower(), message
    else:
        raise SystemExit(f"entrypoint={entrypoint!r}: expected a refused connection")


def not_exported(entrypoint: str) -> None:
    try:
        adbc_driver_manager.dbapi.connect(
            driver=adbc_driver_db2._driver_path(),
            entrypoint=entrypoint,
            db_kwargs={"uri": URI},
        ).close()
    except Exception as e:  # noqa: BLE001
        message = str(e)
        print(f"entrypoint={entrypoint!r} (hidden): {type(e).__name__}: {message[:200]}")
        assert entrypoint in message and "refused" not in message.lower(), message
    else:
        raise SystemExit(f"entrypoint={entrypoint!r}: a hidden name was found")


print(adbc_driver_db2._driver_path())
for name in (None, "AdbcDriverDb2Init", "AdbcDriverInit", "Db2DriverInit"):
    connect_refused(entrypoint=name)
not_exported(entrypoint="Db2DatabaseNew")
print("ok")

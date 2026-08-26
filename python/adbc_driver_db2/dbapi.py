"""
DBAPI 2.0 wrapper for the Db2 ADBC driver.

Mirrors ``adbc_driver_flightsql.dbapi``: opens the underlying ADBC
database via :func:`adbc_driver_db2.connect`, then wraps it in
``adbc_driver_manager.AdbcConnection`` and ``adbc_driver_manager.dbapi.Connection``
so callers get a standard DBAPI 2.0 :class:`Connection`.
"""

from __future__ import annotations

import typing

import adbc_driver_manager
import adbc_driver_manager.dbapi

import adbc_driver_db2

# Re-export DBAPI 2.0 module-level constants + exception hierarchy so callers
# can write `import adbc_driver_db2.dbapi as dbapi; dbapi.Error` etc.
apilevel = adbc_driver_manager.dbapi.apilevel
threadsafety = adbc_driver_manager.dbapi.threadsafety
paramstyle = "qmark"

Warning = adbc_driver_manager.dbapi.Warning
Error = adbc_driver_manager.dbapi.Error
InterfaceError = adbc_driver_manager.dbapi.InterfaceError
DatabaseError = adbc_driver_manager.dbapi.DatabaseError
DataError = adbc_driver_manager.dbapi.DataError
OperationalError = adbc_driver_manager.dbapi.OperationalError
IntegrityError = adbc_driver_manager.dbapi.IntegrityError
InternalError = adbc_driver_manager.dbapi.InternalError
ProgrammingError = adbc_driver_manager.dbapi.ProgrammingError
NotSupportedError = adbc_driver_manager.dbapi.NotSupportedError

Date = adbc_driver_manager.dbapi.Date
Time = adbc_driver_manager.dbapi.Time
Timestamp = adbc_driver_manager.dbapi.Timestamp
DateFromTicks = adbc_driver_manager.dbapi.DateFromTicks
TimeFromTicks = adbc_driver_manager.dbapi.TimeFromTicks
TimestampFromTicks = adbc_driver_manager.dbapi.TimestampFromTicks
STRING = adbc_driver_manager.dbapi.STRING
BINARY = adbc_driver_manager.dbapi.BINARY
NUMBER = adbc_driver_manager.dbapi.NUMBER
DATETIME = adbc_driver_manager.dbapi.DATETIME
ROWID = adbc_driver_manager.dbapi.ROWID

Connection = adbc_driver_manager.dbapi.Connection
Cursor = adbc_driver_manager.dbapi.Cursor

__all__ = [
    "BINARY",
    "Connection",
    "Cursor",
    "DATETIME",
    "DataError",
    "DatabaseError",
    "Date",
    "DateFromTicks",
    "Error",
    "IntegrityError",
    "InterfaceError",
    "InternalError",
    "NUMBER",
    "NotSupportedError",
    "OperationalError",
    "ProgrammingError",
    "ROWID",
    "STRING",
    "Time",
    "TimeFromTicks",
    "Timestamp",
    "TimestampFromTicks",
    "Warning",
    "apilevel",
    "connect",
    "paramstyle",
    "threadsafety",
]


def connect(
    uri: str,
    *,
    username: typing.Optional[str] = None,
    password: typing.Optional[str] = None,
    db_kwargs: typing.Optional[typing.Mapping[str, str]] = None,
    conn_kwargs: typing.Optional[typing.Mapping[str, str]] = None,
    autocommit: bool = False,
) -> "Connection":
    """
    Open a DBAPI 2.0 :class:`Connection` against IBM Db2.

    Parameters
    ----------
    uri:
        Db2 connection URL, e.g. ``"db2://user:pass@host:50000/SAMPLE"``.
    username, password:
        Credentials, if not embedded in the URI.
    db_kwargs:
        Optional mapping of ADBC database options. Common keys:
        ``adbc.db2.token`` for the auth token,
        ``adbc.db2.tls`` to use HTTPS.
    conn_kwargs:
        Optional mapping of ADBC connection options.
    autocommit:
        Forwarded to the underlying ``Connection``.
    """
    db = None
    conn = None
    try:
        db = adbc_driver_db2.connect(
            uri=uri, db_kwargs=db_kwargs, username=username, password=password
        )
        conn = adbc_driver_manager.AdbcConnection(db, **(conn_kwargs or {}))
        return adbc_driver_manager.dbapi.Connection(
            db=db, conn=conn, autocommit=autocommit
        )
    except Exception:
        if conn is not None:
            try:
                conn.close()
            except Exception:
                pass
        if db is not None:
            try:
                db.close()
            except Exception:
                pass
        raise

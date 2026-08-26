"""Apache Arrow ADBC driver for IBM Db2 (pure Go, DRDA wire protocol)."""

from __future__ import annotations

import enum
import functools
import typing

import adbc_driver_manager

from ._version import __version__  # noqa: F401

__all__ = [
    "ConnectionOptions",
    "DatabaseOptions",
    "StatementOptions",
    "connect",
    "install_manifest",
]


class DatabaseOptions(enum.Enum):
    """Database options specific to the Db2 driver.

    All of these can alternatively be given as ``db2://`` URI query
    parameters (``?tls=true&schema=SALES``).
    """

    #: Server host name (alternative to the URI host).
    HOST = "adbc.db2.host"
    #: TCP port (default 50000, or 50001 with TLS).
    PORT = "adbc.db2.port"
    #: Database (RDB) name, e.g. ``"SAMPLE"``.
    DATABASE = "adbc.db2.database"
    #: ``"true"`` to wrap the connection in TLS (required by Db2 on Cloud).
    TLS = "adbc.db2.tls"
    #: Path to a PEM CA certificate bundle for verifying the server.
    TLS_CA_CERT = "adbc.db2.tls.ca_cert"
    #: ``"true"`` to skip server certificate verification.
    TLS_SKIP_VERIFY = "adbc.db2.tls.skip_verify"
    #: DRDA security mechanism: ``"9"`` (encrypted user id + password,
    #: default when the server supports it), ``"3"`` (cleartext password),
    #: ``"4"`` (user id only).
    SECURITY_MECHANISM = "adbc.db2.security_mechanism"
    #: Schema to make current (``SET CURRENT SCHEMA``) after connecting.
    CURRENT_SCHEMA = "adbc.db2.current_schema"
    #: DRDA query block size in bytes (default 1 MiB).
    QUERY_BLOCK_SIZE = "adbc.db2.query_block_size"
    #: Connect timeout: seconds, or a Go duration such as ``"1.5s"``.
    CONNECT_TIMEOUT = "adbc.db2.connect_timeout"
    #: Application name reported to the server.
    APPLICATION_NAME = "adbc.db2.application_name"
    #: Maximum rows per Arrow record batch (default 65536).
    BATCH_SIZE = "adbc.db2.batch_size"
    #: ``"true"`` to log DRDA traffic to stderr.
    TRACE = "adbc.db2.trace"


class ConnectionOptions(enum.Enum):
    """Connection options specific to the Db2 driver."""

    #: Reserved for future use.
    _RESERVED = "adbc.db2.connection.reserved"


class StatementOptions(enum.Enum):
    """Statement options specific to the Db2 driver."""

    #: Reserved for future use.
    _RESERVED = "adbc.db2.statement.reserved"


@functools.lru_cache(maxsize=1)
def _driver_path() -> str:
    """Resolve the path to the bundled c-shared driver library."""
    try:
        from importlib import resources
    except ImportError:  # pragma: no cover
        import importlib_resources as resources  # type: ignore[import-not-found]

    import os
    import sys

    if sys.platform == "darwin":
        candidates = ("libadbc_driver_db2.dylib",)
    elif sys.platform.startswith("win"):
        candidates = ("libadbc_driver_db2.dll", "adbc_driver_db2.dll")
    else:
        candidates = ("libadbc_driver_db2.so",)

    # resources.as_file() does NOT verify that the file exists on disk,
    # so check explicitly; otherwise a stale path reaches the driver
    # manager, which on Windows misparses the drive-letter colon as a
    # `name:entrypoint` separator.
    pkg = resources.files("adbc_driver_db2")
    tried: list[str] = []
    for name in candidates:
        try:
            with resources.as_file(pkg / name) as path:
                resolved = str(path)
                if os.path.isfile(resolved):
                    return resolved
                tried.append(resolved)
        except (FileNotFoundError, ModuleNotFoundError):
            tried.append(name)

    raise FileNotFoundError(
        "Could not locate the bundled Db2 ADBC driver shared library. "
        "Rebuild the wheel with ADBC_DB2_LIBRARY pointing at "
        "libadbc_driver_db2.{so,dylib,dll}. Looked for: " + ", ".join(tried)
    )


def connect(
    uri: str,
    db_kwargs: typing.Mapping[str, str] | None = None,
    *,
    username: str | None = None,
    password: str | None = None,
) -> adbc_driver_manager.AdbcDatabase:
    """
    Open a low-level ADBC database against a Db2 server.

    Most users will want :func:`adbc_driver_db2.dbapi.connect` instead,
    which returns a DBAPI 2.0 :class:`Connection`.

    Parameters
    ----------
    uri:
        ``db2://[user[:password]@]host[:port]/DATABASE[?tls=true&schema=X]``
    db_kwargs:
        Optional mapping of ADBC database options; see
        :class:`DatabaseOptions`.
    username, password:
        Credentials, if not embedded in the URI.
    """
    kwargs = {"driver": _driver_path(), "entrypoint": "Db2DriverInit", "uri": uri}
    if username is not None:
        kwargs["username"] = username
    if password is not None:
        kwargs["password"] = password
    if db_kwargs:
        kwargs.update(db_kwargs)
    return adbc_driver_manager.AdbcDatabase(**kwargs)


def install_manifest(*args, **kwargs):
    """Write the db2.toml ADBC driver manifest; see :mod:`._manifest`.

    After installing the manifest, this driver can be resolved by name —
    via connection profiles (``driver = "db2"``) or directly by URI
    scheme: ``adbc_driver_manager.dbapi.connect(uri="db2://...")``.
    Also available as ``python -m adbc_driver_db2 install-manifest``.
    """
    from ._manifest import install_manifest as _impl

    return _impl(*args, **kwargs)

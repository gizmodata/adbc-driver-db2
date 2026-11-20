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

    The names follow the ADBC driver style guide
    (https://adbc-drivers.org/building-drivers/style-guide.html): a ``db2.``
    prefix, query behaviour under ``db2.query.*``. The pre-0.3.0
    ``adbc.db2.*`` names still work, with a one-time deprecation warning on
    stderr. Most of these can also be given as ``db2://`` URI query
    parameters (``?tls=true&schema=SALES``).
    """

    #: Server host name (alternative to the URI host).
    HOST = "db2.host"
    #: TCP port (default 50000, or 50001 with TLS).
    PORT = "db2.port"
    #: Database (RDB) name, e.g. ``"SAMPLE"``.
    DATABASE = "db2.database"
    #: ``"true"`` to wrap the connection in TLS (required by Db2 on Cloud).
    TLS = "db2.tls"
    #: Path to a PEM CA certificate bundle for verifying the server.
    TLS_CA_CERT = "db2.tls.ca_cert"
    #: ``"true"`` to skip server certificate verification.
    TLS_SKIP_VERIFY = "db2.tls.skip_verify"
    #: DRDA security mechanism: ``"9"`` (encrypted user id + password,
    #: default when the server supports it), ``"3"`` (cleartext password),
    #: ``"4"`` (user id only). An explicit setting fails closed.
    SECURITY_MECHANISM = "db2.auth.security_mechanism"
    #: Schema to make current (``SET CURRENT SCHEMA``) after connecting.
    CURRENT_SCHEMA = "db2.session.current_schema"
    #: Application name reported to the server.
    APPLICATION_NAME = "db2.session.application_name"
    #: Connect timeout in seconds (default 30); a Go duration such as
    #: ``"1.5s"`` also works.
    CONNECT_TIMEOUT = "db2.connect_timeout_seconds"
    #: DRDA query block size in bytes (default 1 MiB).
    QUERY_BLOCK_SIZE = "db2.query.block_size"
    #: Maximum rows per Arrow record batch of a result (default 65536).
    BATCH_SIZE = "db2.query.batch_rows"
    #: Approximate maximum bytes per Arrow record batch (default 8 MiB,
    #: under Flight SQL's 16 MiB gRPC cap; ``"0"`` = only the row cap
    #: applies). Useful for wide rows.
    BATCH_BYTES = "db2.query.batch_bytes"
    #: ``"true"`` to log DRDA traffic to stderr; ``"hex"`` adds payload dumps.
    TRACE = "db2.trace"
    #: Write the DRDA trace to this file (appended) instead of stderr.
    TRACE_FILE = "db2.trace.file"
    #: Dynamic-SQL package as ``COLLECTION.PKGID`` (default
    #: ``NULLID.SYSSH200``), bound automatically when missing.
    PACKAGE = "db2.package"
    #: ``"true"`` to never bind a missing package automatically.
    NO_AUTO_BIND = "db2.package.no_auto_bind"


class ConnectionOptions(enum.Enum):
    """Connection options specific to the Db2 driver."""

    #: Read-only: the DRDA security mechanism negotiated for the session,
    #: e.g. ``"9"`` or ``"3"``.
    SECURITY_MECHANISM_ACTIVE = "db2.auth.security_mechanism_active"


class StatementOptions(enum.Enum):
    """Statement options specific to the Db2 driver (bulk ingest)."""

    #: Length of the ``VARCHAR``/``VARBINARY`` columns bulk ingest creates
    #: (default: sized from the first batch).
    INGEST_VARCHAR_LENGTH = "db2.ingest.varchar_length"
    #: Rows pipelined per DRDA round trip during bulk ingest (default 1000).
    INGEST_BATCH_ROWS = "db2.ingest.batch_rows"


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
    kwargs = {"driver": _driver_path(), "entrypoint": "AdbcDriverDb2Init", "uri": uri}
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

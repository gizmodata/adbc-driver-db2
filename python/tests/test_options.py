"""Option handling through the C library and the driver manager. No server needed."""

from __future__ import annotations

from urllib.parse import quote

import adbc_driver_manager
import pytest

import adbc_driver_db2

PASSWORDS = ["S3cret", "Zqw%ss%Xk7", "Zqw@ss@Xk7", "Zqw/ss/Xk7", "Zqw:ss:Xk7"]


def _database(**kwargs) -> adbc_driver_manager.AdbcDatabase:
    return adbc_driver_manager.AdbcDatabase(
        driver=adbc_driver_db2._driver_path(), entrypoint="AdbcDriverDb2Init", **kwargs
    )


@pytest.mark.parametrize("password", PASSWORDS)
def test_get_option_uri_redacts_the_password(password):
    encoded = quote(password, safe="")
    db = _database(uri=f"db2://admin:{encoded}@127.0.0.1:1/DB?tls=false&pwd={encoded}")
    try:
        uri = db.get_option(key="uri")
        assert uri == "db2://admin:xxxxx@127.0.0.1:1/DB?pwd=xxxxx&tls=false"
        assert password not in uri and encoded not in uri
    finally:
        db.close()


@pytest.mark.parametrize("password", PASSWORDS)
def test_unencoded_password_never_in_the_connect_error(password):
    """Written raw in the URI, a password with %, @, / or : may not parse (or lands
    in the host or port); the error must not show it, or a piece of it."""
    db = _database(uri=f"db2://admin:{password}@127.0.0.1:1/DB")
    try:
        with pytest.raises(adbc_driver_manager.Error) as excinfo:
            adbc_driver_manager.AdbcConnection(db)
        message = str(excinfo.value)
        assert message.split(": ", 1)[1].startswith("[db2] ") or "[db2] " in message
        for piece in ("Zqw", "Xk7", "S3cret"):
            assert piece not in message, message
    finally:
        db.close()


def test_connect_error_keeps_the_canary_text():
    """gizmodata/adbc-install's canary looks for this text."""
    db = _database(uri="db2://user:pass@127.0.0.1:1/database")
    try:
        with pytest.raises(adbc_driver_manager.OperationalError) as excinfo:
            adbc_driver_manager.AdbcConnection(db)
        assert "drda: connect 127.0.0.1:1" in str(excinfo.value)
    finally:
        db.close()

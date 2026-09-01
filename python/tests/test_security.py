"""DRDA security-mechanism behavior.

The community Db2 container authenticates with the LUW default
``SRVCON_AUTH=SERVER``, which only accepts SECMEC 3 (user id + cleartext
password) — that makes it the perfect stage for the fail-closed contract:
an explicitly configured ``adbc.db2.security_mechanism`` must error when
the server refuses it, never silently downgrade to a weaker mechanism.
"""

from __future__ import annotations

import socket
import threading

import pytest

pytestmark = pytest.mark.integration


def _run_via_proxy(server, db_kwargs):
    """Connect through a capturing TCP proxy; return (error, captured bytes)."""
    import adbc_driver_db2.dbapi as db2

    captured = bytearray()
    lock = threading.Lock()
    listener = socket.create_server(("127.0.0.1", 0))
    proxy_port = listener.getsockname()[1]

    def pump(src, dst):
        try:
            while True:
                data = src.recv(65536)
                if not data:
                    break
                with lock:
                    captured.extend(data)
                dst.sendall(data)
        except OSError:
            pass
        finally:
            try:
                dst.shutdown(socket.SHUT_WR)
            except OSError:
                pass

    def serve():
        while True:
            try:
                client, _ = listener.accept()
            except OSError:
                return
            upstream = socket.create_connection((server.host, server.port))
            threading.Thread(target=pump, args=(client, upstream), daemon=True).start()
            threading.Thread(target=pump, args=(upstream, client), daemon=True).start()

    threading.Thread(target=serve, daemon=True).start()
    error = None
    try:
        with db2.connect(
            uri=f"db2://127.0.0.1:{proxy_port}/{server.database}",
            username=server.user,
            password=server.password,
            db_kwargs=db_kwargs,
        ) as conn, conn.cursor() as cur:
            cur.execute("SELECT 1 FROM SYSIBM.SYSDUMMY1")
            cur.fetchall()
    except Exception as exc:  # noqa: BLE001 - the error is an assertion subject
        error = exc
    finally:
        listener.close()
    with lock:
        return error, bytes(captured)


def _password_encodings(password: str) -> list[bytes]:
    # DRDA carries the SECCHK password in EBCDIC on most servers; check
    # ASCII/UTF-8 too so the assertion is encoding-agnostic.
    return [password.encode("utf-8"), password.encode("cp500")]


def test_explicit_encrypted_mechanism_never_sends_cleartext_password(db2_server):
    """The regression this guards: explicit secmec=9 against a server that
    only accepts 3 used to silently downgrade and transmit the password in
    cleartext. Now the connection must fail before any password leaves the
    client, verified at the wire-byte level."""
    # Sanity: under the negotiated default (SECMEC 3 here) the password IS
    # on the wire — which is exactly why the downgrade must not be silent.
    error, wire = _run_via_proxy(db2_server, db_kwargs={})
    if error is not None:
        pytest.skip(f"default connection failed through proxy: {error}")
    assert any(enc in wire for enc in _password_encodings(db2_server.password)), (
        "sanity: expected the cleartext password on the wire under SECMEC 3"
    )

    error, wire = _run_via_proxy(
        db2_server, db_kwargs={"adbc.db2.security_mechanism": "9"}
    )
    assert error is not None, "explicit secmec=9 must fail closed against this server"
    assert "refusing to downgrade" in str(error)
    assert not any(enc in wire for enc in _password_encodings(db2_server.password)), (
        "explicit secmec=9 leaked the password on the wire"
    )


def test_security_mechanism_introspection(db2_server):
    import adbc_driver_db2.dbapi as db2

    with db2.connect(
        uri=db2_server.uri, username=db2_server.user, password=db2_server.password
    ) as conn:
        active = conn.adbc_connection.get_option(key="adbc.db2.security_mechanism_active")
        assert active in ("3", "9")


def test_explicit_accepted_mechanism_still_connects(db2_server):
    import adbc_driver_db2.dbapi as db2

    with db2.connect(
        uri=db2_server.uri,
        username=db2_server.user,
        password=db2_server.password,
        db_kwargs={"adbc.db2.security_mechanism": "3"},
    ) as conn, conn.cursor() as cur:
        assert (
            conn.adbc_connection.get_option(key="adbc.db2.security_mechanism_active")
            == "3"
        )
        cur.execute("SELECT 1 FROM SYSIBM.SYSDUMMY1")
        assert cur.fetchone() == (1,)

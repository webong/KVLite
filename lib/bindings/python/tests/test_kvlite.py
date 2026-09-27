from __future__ import annotations

import os
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from kvlite import HttpDatabase, KVLite, NotFoundError
from kvlite.native import LibraryFinder


class NativeDatabaseTests(unittest.TestCase):
    def test_finds_installed_driver_in_system_catalog(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            library = Path(root) / "drivers" / "leveldb" / "lib" / LibraryFinder._library_name()
            library.parent.mkdir(parents=True)
            shutil.copyfile(os.environ["KVLITE_TEST_LIBRARY"], library)
            with patch.dict(os.environ, {"KVLITE_SYSTEM_MODULE_PATH": root, "KVLITE_LIBRARY_PATH": "", "KVLITE_HOME": ""}):
                self.assertEqual(LibraryFinder.find(driver="leveldb"), library.resolve())

    def test_json_and_binary_round_trip(self) -> None:
        database = KVLite.open("/tmp/kvlite-python-mock", os.environ["KVLITE_TEST_LIBRARY"], driver="leveldb")
        self.addCleanup(database.close)

        database.put("user:101", {"id": 101, "name": "Ada"}, ttl_seconds=60)
        self.assertEqual(database.get("user:101"), {"id": 101, "name": "Ada"})

        database.put_bytes(b"binary\x00key", b"value\x00bytes")
        self.assertEqual(database.get_bytes(b"binary\x00key"), b"value\x00bytes")
        database.delete(b"binary\x00key")
        with self.assertRaises(NotFoundError):
            database.get_bytes(b"binary\x00key")
        database.close()

        # `backend` remains an embedded-call compatibility alias.
        legacy = KVLite.open("/tmp/kvlite-python-legacy-backend", os.environ["KVLITE_TEST_LIBRARY"], backend="leveldb")
        self.addCleanup(legacy.close)


class HttpDatabaseTests(unittest.TestCase):
    def test_json_protocol_uses_ttl_and_bearer_token(self) -> None:
        requests: list[tuple[str, str, bytes | None, dict[str, str]]] = []

        def requester(method: str, url: str, body: bytes | None, headers: dict[str, str]) -> tuple[int, bytes]:
            requests.append((method, url, body, headers))
            if method == "GET":
                return 200, b'{"enabled":true}'
            return 204, b""

        database = HttpDatabase("http://127.0.0.1:8089", token="secret", driver="leveldb", requester=requester)
        database.put("flags:101", {"enabled": True}, ttl_seconds=30)
        self.assertEqual(database.get("flags:101"), {"enabled": True})
        database.delete("flags:101")

        self.assertEqual(len(requests), 3)
        self.assertIn("ttl_seconds=30", requests[0][1])
        self.assertEqual(requests[0][3]["Authorization"], "Bearer secret")
        self.assertEqual(requests[0][3]["X-KVLite-Driver"], "leveldb")


if __name__ == "__main__":
    unittest.main()

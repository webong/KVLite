"""Open KVLite in this process; no server or network transport is involved."""

import os
from pathlib import Path

import kvlite


path = os.environ.get("KVLITE_DB_PATH", str(Path(__file__).parent / "data"))
driver = os.environ.get("KVLITE_DRIVER", "leveldb")

with kvlite.open(path, driver=driver) as db:
    db.put("user:101", {"id": 101, "name": "Ada"}, ttl_seconds=3600)
    print("user:", db.get("user:101"))

    db.put_bytes("blob:101", b"\x00\x01\x02")
    print("bytes:", db.get_bytes("blob:101").hex())

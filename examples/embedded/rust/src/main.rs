use std::env;
use std::time::Duration;

use kvlite::Database;
use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize, Serialize)]
struct User {
    id: u64,
    name: String,
}

fn main() -> kvlite::Result<()> {
    let path = env::var("KVLITE_DB_PATH").unwrap_or_else(|_| "./data".into());
    let driver = env::var("KVLITE_DRIVER").unwrap_or_else(|_| "leveldb".into());
    let mut db = Database::open_with_driver(path, driver)?;

    db.put(
        "user:101",
        &User {
            id: 101,
            name: "Ada".into(),
        },
        Some(Duration::from_secs(3600)),
    )?;
    let user: User = db.get("user:101")?;
    println!("user: {user:?}");

    db.put_bytes("blob:101", [0, 1, 2], None)?;
    println!("bytes: {:?}", db.get_bytes("blob:101")?);
    db.close()
}

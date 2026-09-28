use std::env;
use std::fs;
use std::path::PathBuf;
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use kvlite::{Database, Error, LibraryFinder};
use serde_json::json;

struct MockLibrary {
    directory: PathBuf,
    path: PathBuf,
}

impl MockLibrary {
    fn compile() -> Self {
        let directory = env::temp_dir().join(format!(
            "kvlite-rust-test-{}-{}",
            std::process::id(),
            SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos(),
        ));
        fs::create_dir_all(&directory).unwrap();
        let source = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../test-fixtures/mock_kvlite.c");
        let path = if cfg!(target_os = "macos") {
            directory.join("libkvlite.dylib")
        } else if cfg!(target_os = "windows") {
            directory.join("kvlite.dll")
        } else {
            directory.join("libkvlite.so")
        };
        let mut command = Command::new("cc");
        if cfg!(target_os = "macos") {
            command.args(["-dynamiclib", "-fPIC"]);
        } else if cfg!(target_os = "windows") {
            panic!("mock C library test is not implemented on Windows yet");
        } else {
            command.args(["-shared", "-fPIC"]);
        }
        let status = command.arg(source).arg("-o").arg(&path).status().unwrap();
        assert!(status.success(), "failed to compile ABI mock");
        Self { directory, path }
    }
}

impl Drop for MockLibrary {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.directory);
    }
}

#[test]
fn native_json_and_binary_round_trip() {
    let mock = MockLibrary::compile();
    let mut database = Database::open_with_library_and_backend("/tmp/kvlite-rust-mock", &mock.path, "leveldb").unwrap();
    database.put("user:101", &json!({"id": 101, "name": "Ada"}), None).unwrap();
    let user: serde_json::Value = database.get("user:101").unwrap();
    assert_eq!(user, json!({"id": 101, "name": "Ada"}));

    database.put_bytes(b"binary\0key", b"value\0bytes", None).unwrap();
    assert_eq!(database.get_bytes(b"binary\0key").unwrap(), b"value\0bytes");
    database.delete(b"binary\0key").unwrap();
    assert!(matches!(database.get_bytes(b"binary\0key"), Err(Error::NotFound(_))));
    database.close().unwrap();
}

#[test]
fn finds_installed_driver_in_system_catalog() {
    let mock = MockLibrary::compile();
    let catalog = mock.directory.join("catalog");
    let installed = catalog.join("drivers/leveldb/lib").join(mock.path.file_name().unwrap());
    fs::create_dir_all(installed.parent().unwrap()).unwrap();
    fs::copy(&mock.path, &installed).unwrap();
    let host = catalog.join("lib").join(mock.path.file_name().unwrap());
    fs::create_dir_all(host.parent().unwrap()).unwrap();
    fs::copy(&mock.path, &host).unwrap();
    let previous_catalog = env::var_os("KVLITE_SYSTEM_MODULE_PATH");
    let previous_library = env::var_os("KVLITE_LIBRARY_PATH");
    let previous_home = env::var_os("KVLITE_HOME");
    env::set_var("KVLITE_SYSTEM_MODULE_PATH", &catalog);
    env::remove_var("KVLITE_LIBRARY_PATH");
    env::remove_var("KVLITE_HOME");
    let expected = if cfg!(all(target_os = "macos", target_arch = "x86_64")) { installed } else { host };
    assert_eq!(LibraryFinder::find(None, Some("leveldb")).unwrap(), expected);
    match previous_catalog { Some(value) => env::set_var("KVLITE_SYSTEM_MODULE_PATH", value), None => env::remove_var("KVLITE_SYSTEM_MODULE_PATH") }
    match previous_library { Some(value) => env::set_var("KVLITE_LIBRARY_PATH", value), None => env::remove_var("KVLITE_LIBRARY_PATH") }
    match previous_home { Some(value) => env::set_var("KVLITE_HOME", value), None => env::remove_var("KVLITE_HOME") }
}

use std::env;
use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use kvlite::{Database, Error, LibraryFinder};
use serde_json::json;

/// Distinguishes concurrent test threads, whose directory names would
/// otherwise collide on a coarse system clock.
static MOCK_SEQUENCE: AtomicU64 = AtomicU64::new(0);

struct MockLibrary {
    directory: PathBuf,
    path: PathBuf,
}

impl MockLibrary {
    fn compile() -> Self {
        let unique = MOCK_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let directory = env::temp_dir().join(format!(
            "kvlite-rust-test-{}-{}-{}",
            std::process::id(),
            SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos(),
            unique,
        ));
        fs::create_dir_all(&directory).unwrap();
        // macOS exposes the temporary directory through the `/var` symlink;
        // canonicalize once so path assertions and dynamic loading agree.
        let directory = directory.canonicalize().unwrap();
        let source = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../test-fixtures/mock_kvlite.c");
        let path = if cfg!(target_os = "macos") {
            directory.join("libkvlite.dylib")
        } else if cfg!(target_os = "windows") {
            directory.join("kvlite.dll")
        } else {
            directory.join("libkvlite.so")
        };
        compile_shared_library(&source, &path);
        assert!(
            path.is_file(),
            "the C compiler did not produce the ABI mock at {}",
            path.display(),
        );
        Self { directory, path }
    }
}

/// Compile the ABI mock into a loadable shared library.
///
/// `CC` selects the compiler and may carry leading arguments (for example
/// `cc -m64`). MSVC's `cl` needs `/LD` and the environment that a Developer
/// Command Prompt or a `vcvarsall`-style CI setup provides; a bare `cl` on
/// `PATH` will report its own missing-header error.
fn compile_shared_library(source: &Path, path: &Path) {
    let (program, mut arguments) = compiler_command();
    if is_msvc(&program) {
        arguments.push(OsString::from("/nologo"));
        arguments.push(OsString::from("/LD"));
        arguments.push(OsString::from(format!("/Fe:{}", path.display())));
    } else if cfg!(target_os = "macos") {
        arguments.push(OsString::from("-dynamiclib"));
        arguments.push(OsString::from("-fPIC"));
        arguments.push(OsString::from("-o"));
        arguments.push(path.as_os_str().to_owned());
    } else {
        arguments.push(OsString::from("-shared"));
        arguments.push(OsString::from("-fPIC"));
        arguments.push(OsString::from("-o"));
        arguments.push(path.as_os_str().to_owned());
    }
    arguments.push(source.as_os_str().to_owned());

    let status = Command::new(&program)
        .args(&arguments)
        .status()
        .unwrap_or_else(|error| {
            panic!(
                "could not run the C compiler {:?} ({error}); set CC to a working compiler",
                program,
            )
        });
    assert!(
        status.success(),
        "the C compiler {:?} failed to build the KVLite ABI mock",
        program,
    );
}

/// Return the compiler program and any arguments embedded in `CC`.
fn compiler_command() -> (OsString, Vec<OsString>) {
    let configured = env::var_os("CC");
    let value = configured.as_deref().unwrap_or_else(|| {
        if cfg!(target_os = "windows") && !cfg!(target_env = "gnu") {
            std::ffi::OsStr::new("cl")
        } else {
            std::ffi::OsStr::new("cc")
        }
    });
    let value = value.to_string_lossy();
    let mut parts = value.split_whitespace();
    let program = OsString::from(parts.next().unwrap_or("cc"));
    let arguments = parts.map(OsString::from).collect();
    (program, arguments)
}

fn is_msvc(program: &OsString) -> bool {
    let program = program.to_string_lossy().to_lowercase();
    let file_name = program.rsplit(['/', '\\']).next().unwrap_or(&program);
    let stem = file_name.strip_suffix(".exe").unwrap_or(file_name);
    matches!(stem, "cl" | "clang-cl")
}

impl Drop for MockLibrary {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.directory);
    }
}

#[test]
fn native_json_and_binary_round_trip() {
    let mock = MockLibrary::compile();
    let database_path = mock.directory.join("database");
    let mut database = Database::open_with_library_and_backend(&database_path, &mock.path, "leveldb").unwrap();
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

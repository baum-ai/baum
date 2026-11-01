//! Native settings stay available before the daemon starts. The app's
//! single-instance plugin leaves one process writing this file; the shared
//! lock also serializes its setup, notification, zoom and updater threads.

use std::fs::{self, OpenOptions};
use std::io::{self, Write};
use std::path::Path;
use std::sync::Mutex;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::{Map, Value};

pub const FILE: &str = "desktop.json";
static FILE_LOCK: Mutex<()> = Mutex::new(());
static NEXT_FILE: AtomicU64 = AtomicU64::new(0);

fn document(path: &Path) -> io::Result<Map<String, Value>> {
    let data = match fs::read(path) {
        Ok(data) => data,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Map::new()),
        Err(e) => return Err(e),
    };
    match serde_json::from_slice(&data)? {
        Value::Object(object) => Ok(object),
        _ => Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "desktop settings must be a JSON object",
        )),
    }
}

fn section<T: DeserializeOwned + Default>(
    object: &Map<String, Value>,
    name: &str,
) -> io::Result<T> {
    match object.get(name) {
        Some(value @ Value::Object(_)) => serde_json::from_value(value.clone()).map_err(Into::into),
        Some(_) => Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "desktop settings section must be a JSON object",
        )),
        None => Ok(T::default()),
    }
}

/// Strict reads let a native action refuse a damaged file before changing
/// anything whose installation record would have to be saved there.
pub fn read<T: DeserializeOwned + Default>(path: &Path, name: &str) -> io::Result<T> {
    let _one = FILE_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    section(&document(path)?, name)
}

/// A bad setting must not leave the window unreadable or prevent startup.
/// Writes still refuse the damaged file, preserving all its other settings.
pub fn load<T: DeserializeOwned + Default>(path: &Path, name: &str) -> T {
    read(path, name).unwrap_or_else(|e| {
        eprintln!("baum desktop: {name} settings unreadable, using defaults: {e}");
        T::default()
    })
}

/// Reads the latest section and replaces just that section under one lock.
/// Unrelated sections, including ones this caller does not know, survive.
pub fn update<T, R>(path: &Path, name: &str, change: impl FnOnce(&mut T) -> R) -> io::Result<R>
where
    T: DeserializeOwned + Serialize + Default,
{
    let _one = FILE_LOCK.lock().unwrap_or_else(|p| p.into_inner());
    let mut object = document(path)?;
    let mut saved = section(&object, name)?;
    let result = change(&mut saved);
    object.insert(name.into(), serde_json::to_value(saved)?);
    write(path, &serde_json::to_vec_pretty(&object)?)?;
    Ok(result)
}

fn write(path: &Path, data: &[u8]) -> io::Result<()> {
    let dir = path.parent().ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidInput,
            "desktop settings have no directory",
        )
    })?;
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder.create(dir)?;
    let stamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    let temporary = dir.join(format!(
        ".{FILE}.{}.{stamp}.{}",
        std::process::id(),
        NEXT_FILE.fetch_add(1, Ordering::Relaxed)
    ));
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(&temporary)?;
    let result = (|| {
        file.write_all(data)?;
        file.sync_all()?;
        drop(file);
        fs::rename(&temporary, path)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Debug, Default, PartialEq, Serialize, Deserialize)]
    #[serde(default)]
    struct Count {
        value: usize,
    }

    fn scratch(name: &str) -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "baum-desktop-settings-{name}-{}",
            std::process::id()
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn sections_survive_reopening_and_preserve_unrelated_data() {
        let dir = scratch("sections");
        let path = dir.join(FILE);
        fs::write(&path, r#"{"future":{"keep":"this"}}"#).unwrap();
        for name in ["setup", "notifications", "window", "updates"] {
            update::<Count, _>(&path, name, |saved| saved.value = 7).unwrap();
        }
        for name in ["setup", "notifications", "window", "updates"] {
            assert_eq!(read::<Count>(&path, name).unwrap().value, 7);
        }
        assert_eq!(document(&path).unwrap()["future"]["keep"], "this");
        assert_eq!(fs::read_dir(&dir).unwrap().count(), 1);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(&path).unwrap().permissions().mode() & 0o777,
                0o600
            );
        }
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn concurrent_updates_keep_every_change_in_each_section() {
        let dir = scratch("concurrent");
        let path = dir.join(FILE);
        std::thread::scope(|scope| {
            for name in ["setup", "window", "setup", "updates", "window", "updates"] {
                let path = &path;
                scope.spawn(move || {
                    for _ in 0..20 {
                        update::<Count, _>(path, name, |saved| saved.value += 1).unwrap();
                    }
                });
            }
        });
        for name in ["setup", "window", "updates"] {
            assert_eq!(read::<Count>(&path, name).unwrap().value, 40);
        }
        assert_eq!(fs::read_dir(&dir).unwrap().count(), 1);
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn damaged_documents_and_sections_are_never_overwritten() {
        let dir = scratch("damaged");
        let path = dir.join(FILE);
        for data in [
            "{",
            "[]",
            "null",
            r#"{"window":"bad","setup":{"value":9}}"#,
            r#"{"window":[],"setup":{"value":9}}"#,
            r#"{"window":null,"setup":{"value":9}}"#,
            r#"{"window":true,"setup":{"value":9}}"#,
            r#"{"window":5,"setup":{"value":9}}"#,
        ] {
            fs::write(&path, data).unwrap();
            assert_eq!(load::<Count>(&path, "window"), Count::default());
            assert!(update::<Count, _>(&path, "window", |saved| saved.value = 3).is_err());
            assert_eq!(fs::read_to_string(&path).unwrap(), data);
        }
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn a_failed_replacement_cleans_up_and_preserves_the_destination() {
        let dir = scratch("failure");
        let path = dir.join(FILE);
        fs::create_dir(&path).unwrap();
        let sentinel = path.join("keep");
        fs::write(&sentinel, "unchanged").unwrap();
        assert!(write(&path, b"{}").is_err());
        assert_eq!(fs::read_to_string(sentinel).unwrap(), "unchanged");
        assert_eq!(fs::read_dir(&dir).unwrap().count(), 1);
        let _ = fs::remove_dir_all(dir);
    }
}

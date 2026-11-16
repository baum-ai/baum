//! Saving a tile type file the Tile library exported. The daemon's pages
//! (the `daemon-pages` capability) may call it: it opens the native save
//! dialog and writes only where the person chose, only a file of the size
//! and name a tile type file has.

use tauri::AppHandle;

/// The largest file the Tile library exports or imports.
const MAX_FILE_BYTES: usize = 3 << 20;

/// Whether `name` is a tile type's name: a lowercase letter, then up to 63
/// lowercase letters, digits and underscores.
fn valid_name(name: &str) -> bool {
    let mut chars = name.chars();
    matches!(chars.next(), Some('a'..='z'))
        && name.len() <= 64
        && chars.all(|c| matches!(c, 'a'..='z' | '0'..='9' | '_'))
}

/// Asks where to save `<name>.baumtile.json` and writes `contents` there.
/// Returns the path written, or none when the person cancelled.
#[tauri::command]
pub async fn save_tile_type(
    app: AppHandle,
    name: String,
    contents: String,
) -> Result<Option<String>, String> {
    use tauri_plugin_dialog::DialogExt;
    if !valid_name(&name) {
        return Err("That is not a tile type's name.".into());
    }
    if contents.len() > MAX_FILE_BYTES {
        return Err("The file is larger than 3 MiB.".into());
    }
    tauri::async_runtime::spawn_blocking(move || {
        let Some(path) = app
            .dialog()
            .file()
            .set_title("Export tile type")
            .set_file_name(format!("{name}.baumtile.json"))
            .add_filter("baum tile type", &["json"])
            .blocking_save_file()
        else {
            return Ok(None);
        };
        let path = path.into_path().map_err(|e| e.to_string())?;
        std::fs::write(&path, contents.as_bytes()).map_err(|e| e.to_string())?;
        Ok(Some(path.to_string_lossy().into_owned()))
    })
    .await
    .map_err(|e| e.to_string())?
}

#[cfg(test)]
mod tests {
    #[test]
    fn names_are_tile_type_names() {
        assert!(super::valid_name("migration_steps"));
        assert!(super::valid_name("a1"));
        for bad in ["", "1a", "Mig", "../x", "a/b", "a.b", &"a".repeat(65)] {
            assert!(!super::valid_name(bad), "{bad}");
        }
    }
}

//! Debug-only bridge to the container's Vite server. No credentials leave
//! the normal fragment/cookie exchange; the shared file holds only an origin.

pub fn frontend(launch: &url::Url) -> Result<url::Url, String> {
    if !cfg!(debug_assertions) {
        return Ok(launch.clone());
    }
    let Some(directory) = std::env::var_os("BAUM_DESKTOP_DEV_DIR") else {
        return Ok(launch.clone());
    };
    let directory = std::path::PathBuf::from(directory);
    let pending = directory.join("daemon-origin.tmp");
    std::fs::write(&pending, launch.origin().ascii_serialization())
        .and_then(|()| std::fs::rename(&pending, directory.join("daemon-origin")))
        .map_err(|e| format!("Cannot configure the development proxy: {e}"))?;
    let mut frontend = launch.clone();
    frontend
        .set_port(Some(5173))
        .map_err(|()| "Cannot set the development frontend port".to_string())?;
    Ok(frontend)
}

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

/// Vite's copy of the setup screen, so edits reload in place. Debug builds
/// under `desktop::dev` only; otherwise the bundled page shows.
pub fn setup_page() -> Option<url::Url> {
    if !cfg!(debug_assertions) || std::env::var_os("BAUM_DESKTOP_DEV_DIR").is_none() {
        return None;
    }
    Some(url::Url::parse(SETUP_PAGE).expect("static URL"))
}

// `localhost`, not `127.0.0.1`: Tauri checks a page's origin, never its
// path, and the daemon's pages use Vite on `127.0.0.1`.
const SETUP_PAGE: &str = "http://localhost:5173/setup.html";

/// Whether `url` is Vite's setup screen, ignoring only the fragment.
pub fn is_setup_page(url: &url::Url) -> bool {
    setup_page().is_some_and(|page| {
        url.origin() == page.origin() && url.path() == page.path() && url.query().is_none()
    })
}

/// Grants Vite's setup screen the bundled screen's commands. Its origin is
/// its own, so the daemon's pages get none; navigation reaches only the
/// setup page on it.
pub fn grant_setup(app: &tauri::AppHandle) -> tauri::Result<()> {
    if setup_page().is_none() {
        return Ok(());
    }
    let mut capability = tauri::ipc::CapabilityBuilder::new("setup-screen-development")
        .remote("http://localhost:5173/*".to_string())
        .local(false)
        .window(crate::MAIN);
    for permission in crate::onboarding::SETUP_PERMISSIONS {
        capability = capability.permission(*permission);
    }
    tauri::Manager::add_capability(app, capability)
}

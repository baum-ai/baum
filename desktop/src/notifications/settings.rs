//! The notification settings the app keeps in its config directory: which
//! kinds are on (all, by default) and what the operating system answered when
//! the app first asked to notify. Nothing here is secret.

#[cfg(test)]
use std::fs;
use std::io;
use std::path::PathBuf;

use serde::{Deserialize, Serialize};

use super::toast::Kind;

const SECTION: &str = "notifications";

/// What the operating system lets the app do.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Permission {
    /// Not asked yet: the app asks when it first has something to show.
    #[default]
    Unknown,
    Granted,
    Denied,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Kinds {
    pub finished: bool,
    pub failed: bool,
    pub requested: bool,
    pub automatic: bool,
    pub checks_failed: bool,
    pub conflict: bool,
    pub changes_requested: bool,
    pub ready_to_merge: bool,
    pub fix_pushed: bool,
    pub fix_failed: bool,
    pub session_permission: bool,
    pub session_input: bool,
}

impl Default for Kinds {
    fn default() -> Self {
        Kinds {
            finished: true,
            failed: true,
            requested: true,
            automatic: true,
            checks_failed: true,
            conflict: true,
            changes_requested: true,
            ready_to_merge: true,
            fix_pushed: true,
            fix_failed: true,
            session_permission: true,
            session_input: true,
        }
    }
}

impl Kinds {
    pub fn get(&self, kind: Kind) -> bool {
        match kind {
            Kind::Finished => self.finished,
            Kind::Failed => self.failed,
            Kind::Requested => self.requested,
            Kind::Automatic => self.automatic,
            Kind::ChecksFailed => self.checks_failed,
            Kind::Conflict => self.conflict,
            Kind::ChangesRequested => self.changes_requested,
            Kind::ReadyToMerge => self.ready_to_merge,
            Kind::FixPushed => self.fix_pushed,
            Kind::FixFailed => self.fix_failed,
            Kind::SessionPermission => self.session_permission,
            Kind::SessionInput => self.session_input,
        }
    }

    pub fn set(&mut self, kind: Kind, on: bool) {
        match kind {
            Kind::Finished => self.finished = on,
            Kind::Failed => self.failed = on,
            Kind::Requested => self.requested = on,
            Kind::Automatic => self.automatic = on,
            Kind::ChecksFailed => self.checks_failed = on,
            Kind::Conflict => self.conflict = on,
            Kind::ChangesRequested => self.changes_requested = on,
            Kind::ReadyToMerge => self.ready_to_merge = on,
            Kind::FixPushed => self.fix_pushed = on,
            Kind::FixFailed => self.fix_failed = on,
            Kind::SessionPermission => self.session_permission = on,
            Kind::SessionInput => self.session_input = on,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Settings {
    pub kinds: Kinds,
    pub permission: Permission,
}

/// Where the settings live; `None` keeps them in memory only.
#[derive(Debug, Clone, Default)]
pub struct Store {
    path: Option<PathBuf>,
}

impl Store {
    pub fn in_dir(dir: PathBuf) -> Store {
        Store {
            path: Some(dir.join(crate::settings::FILE)),
        }
    }

    #[cfg(test)]
    pub fn in_memory() -> Store {
        Store { path: None }
    }

    /// The saved settings; a missing or unreadable file is the defaults, so
    /// a damaged file never keeps the app from notifying.
    pub fn load(&self) -> Settings {
        let Some(path) = &self.path else {
            return Settings::default();
        };
        crate::settings::load(path, SECTION)
    }

    pub fn save(&self, settings: &Settings) -> io::Result<()> {
        let Some(path) = &self.path else {
            return Ok(());
        };
        crate::settings::update(path, SECTION, |current: &mut Settings| {
            *current = settings.clone()
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("baum-desktop-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        dir
    }

    #[test]
    fn everything_is_on_and_unasked_by_default() {
        let settings = Store::in_dir(scratch("default")).load();
        assert!(Kind::ALL.into_iter().all(|k| settings.kinds.get(k)));
        assert_eq!(settings.permission, Permission::Unknown);
    }

    #[test]
    fn settings_survive_a_restart() {
        let dir = scratch("roundtrip");
        let store = Store::in_dir(dir.clone());
        let mut settings = store.load();
        settings.kinds.set(Kind::Automatic, false);
        settings.permission = Permission::Denied;
        store.save(&settings).unwrap();
        let again = Store::in_dir(dir.clone()).load();
        assert_eq!(again, settings);
        assert!(!again.kinds.get(Kind::Automatic) && again.kinds.get(Kind::Failed));
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn unspecified_kinds_are_on() {
        let dir = scratch("unspecified");
        fs::create_dir_all(&dir).unwrap();
        fs::write(
            dir.join(crate::settings::FILE),
            r#"{"notifications":{"kinds":{"failed":false}}}"#,
        )
        .unwrap();
        let settings = Store::in_dir(dir.clone()).load();
        assert!(!settings.kinds.failed && settings.kinds.finished && settings.kinds.automatic);
        // The other kinds keep their defaults.
        assert!(
            [
                Kind::ChecksFailed,
                Kind::Conflict,
                Kind::ChangesRequested,
                Kind::ReadyToMerge,
                Kind::FixPushed,
                Kind::FixFailed,
                Kind::SessionPermission,
                Kind::SessionInput
            ]
            .into_iter()
            .all(|k| settings.kinds.get(k))
        );
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn a_damaged_file_is_the_defaults() {
        let dir = scratch("damaged");
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join(crate::settings::FILE), "{").unwrap();
        let store = Store::in_dir(dir.clone());
        assert_eq!(store.load(), Settings::default());
        assert!(store.save(&Settings::default()).is_err());
        assert_eq!(
            fs::read_to_string(dir.join(crate::settings::FILE)).unwrap(),
            "{"
        );
        let _ = fs::remove_dir_all(dir);
    }
}

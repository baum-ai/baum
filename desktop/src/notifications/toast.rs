//! What a daemon event says, as the notification the user sees. Everything
//! here is a pure function of the event: the text comes only from fields the
//! daemon already redacted (the repository, the title as GitHub returned it,
//! the stored risk score, a failure category) and is only stripped of control
//! characters and bounded in length, never reworded.

use serde::Deserialize;
use url::Url;

/// A kind of notification, the unit of the per-kind settings.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Kind {
    Finished,
    Failed,
    Requested,
    CapReached,
    Automatic,
}

impl Kind {
    pub const ALL: [Kind; 5] = [
        Kind::Finished,
        Kind::Failed,
        Kind::Requested,
        Kind::CapReached,
        Kind::Automatic,
    ];

    /// The menu entry's label.
    pub fn label(self) -> &'static str {
        match self {
            Kind::Finished => "Review finished",
            Kind::Failed => "Review failed",
            Kind::Requested => "Review requested from me",
            Kind::CapReached => "Daily cost cap reached",
            Kind::Automatic => "Review generated automatically",
        }
    }

    /// The menu entry's identifier.
    pub fn menu_id(self) -> &'static str {
        match self {
            Kind::Finished => "notifications:finished",
            Kind::Failed => "notifications:failed",
            Kind::Requested => "notifications:requested",
            Kind::CapReached => "notifications:cap_reached",
            Kind::Automatic => "notifications:automatic",
        }
    }

    pub fn from_menu_id(id: &str) -> Option<Kind> {
        Kind::ALL.into_iter().find(|kind| kind.menu_id() == id)
    }
}

/// Where a click opens the app.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Route {
    /// A pull request's page in the clone the daemon named.
    Pr { clone: String, number: u64 },
    /// A picked repository that has no clone yet.
    RepositoryPr { repository: String, number: u64 },
    /// The jobs list.
    Jobs,
}

impl Route {
    /// The URL under the daemon's app path (`base`, which ends in a slash).
    pub fn url(&self, base: &Url) -> Url {
        let mut url = base.clone();
        url.set_fragment(None);
        {
            let mut query = url.query_pairs_mut();
            query.clear();
            match self {
                Route::Pr { clone, number } => {
                    query.append_pair("clone", clone);
                    query.append_pair("pr", &number.to_string());
                }
                Route::RepositoryPr { repository, number } => {
                    query.append_pair("repository", repository);
                    query.append_pair("pr", &number.to_string());
                }
                Route::Jobs => {
                    query.append_pair("page", "jobs");
                }
            }
        }
        url
    }

    /// The pull request this route shows, if it is one.
    pub fn pr(&self) -> Option<(&str, u64)> {
        match self {
            Route::Pr { clone, number } => Some((clone, *number)),
            Route::RepositoryPr { repository, number } => Some((repository, *number)),
            Route::Jobs => None,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Toast {
    pub kind: Kind,
    pub title: String,
    pub body: String,
    pub route: Route,
    /// An event that repeats while its condition lasts names itself, so it
    /// is shown once.
    pub dedupe: Option<String>,
}

/// The daemon's `notice` event.
#[derive(Debug, Deserialize)]
struct Notice {
    kind: String,
    #[serde(default)]
    repository: String,
    #[serde(default)]
    pr: u64,
    #[serde(default)]
    clone: Option<String>,
    #[serde(default)]
    title: Option<String>,
    #[serde(default)]
    risk: Option<u8>,
    #[serde(default)]
    risk_status: Option<String>,
    #[serde(default)]
    reason: Option<String>,
}

/// The daemon's `automatic` event: one repository's automatic generation.
#[derive(Debug, Deserialize)]
struct Automatic {
    #[serde(default)]
    repository: String,
    #[serde(default)]
    daily_cap: Option<f64>,
    #[serde(default)]
    spent_today: f64,
    #[serde(default)]
    paused_until: Option<String>,
    #[serde(default)]
    notice: Option<String>,
}

const MAX_TITLE: usize = 200;
const MAX_FIELD: usize = 120;

/// The notification an event calls for, if any. Unknown events, kinds and
/// malformed payloads call for none.
pub fn from_event(name: &str, data: &str) -> Option<Toast> {
    match name {
        "notice" => from_notice(serde_json::from_str(data).ok()?),
        "automatic" => from_automatic(serde_json::from_str(data).ok()?),
        _ => None,
    }
}

fn from_notice(n: Notice) -> Option<Toast> {
    let kind = match n.kind.as_str() {
        "review_finished" => Kind::Finished,
        "review_failed" => Kind::Failed,
        "review_requested" => Kind::Requested,
        "review_automatic" => Kind::Automatic,
        _ => return None,
    };
    if n.pr == 0 {
        return None;
    }
    let repository = clean(&n.repository, MAX_FIELD);
    let risk = n.risk.filter(|r| (1..=5).contains(r)).map(|score| {
        if n.risk_status.as_deref() == Some("lower_bound") {
            format!("risk at least {score} of 5")
        } else {
            format!("risk {score} of 5")
        }
    });
    let headline = match kind {
        Kind::Finished => "Review finished",
        Kind::Automatic => "Review generated automatically",
        Kind::Failed => "Review failed",
        Kind::Requested => "Review requested from you",
        Kind::CapReached => unreachable!("a notice never names the cap"),
    };
    let title = match (kind, &risk, n.reason.as_deref()) {
        (Kind::Finished | Kind::Automatic, Some(risk), _) => format!("{headline}: {risk}"),
        (Kind::Failed, _, Some(reason)) if !reason.is_empty() => {
            format!(
                "{headline}: {}",
                clean(&reason.replace(['_', '-'], " "), 60)
            )
        }
        _ => headline.to_string(),
    };
    let reference = format!("{repository} #{}", n.pr);
    let body = match n.title.as_deref().map(|t| clean(t, MAX_TITLE)) {
        Some(t) if !t.is_empty() => format!("{t}\n{reference}"),
        _ => reference,
    };
    let route = match n.clone.filter(|c| !c.is_empty()) {
        Some(clone) => Route::Pr {
            clone,
            number: n.pr,
        },
        None if !n.repository.is_empty() => Route::RepositoryPr {
            repository: n.repository,
            number: n.pr,
        },
        None => Route::Jobs,
    };
    Some(Toast {
        kind,
        title,
        body,
        route,
        dedupe: None,
    })
}

fn from_automatic(a: Automatic) -> Option<Toast> {
    // No default cap exists: a pause for the cap means the user set one.
    let cap = a.daily_cap?;
    let until = a.paused_until?;
    if a.notice.as_deref() != Some("daily_cap") {
        return None;
    }
    let repository = clean(&a.repository, MAX_FIELD);
    Some(Toast {
        kind: Kind::CapReached,
        title: "Daily cost cap reached".into(),
        body: format!(
            "Automatic reviews for {repository} are paused. Spent ${:.2} of the ${cap:.2} daily cap.",
            a.spent_today
        ),
        route: Route::Jobs,
        dedupe: Some(format!("cap:{}:{until}", a.repository.to_lowercase())),
    })
}

/// Unicode format characters (category Cf): bidirectional overrides and
/// isolates, zero-width characters, the byte order mark and tag characters.
/// They render nothing but can reorder or hide the text around them.
fn is_format(c: char) -> bool {
    matches!(u32::from(c),
        0xAD | 0x600..=0x605 | 0x61C | 0x6DD | 0x70F | 0x890..=0x891 | 0x8E2 | 0x180E
        | 0x200B..=0x200F | 0x202A..=0x202E | 0x2060..=0x2064 | 0x2066..=0x206F | 0xFEFF
        | 0xFFF9..=0xFFFB | 0x110BD | 0x110CD | 0x13430..=0x1343F | 0x1BCA0..=0x1BCA3
        | 0x1D173..=0x1D17A | 0xE0001 | 0xE0020..=0xE007F)
}

/// Format characters are dropped, control characters become spaces and runs of whitespace one space; the
/// result is at most `max` characters.
fn clean(text: &str, max: usize) -> String {
    let spaced: String = text
        .chars()
        .filter(|c| !is_format(*c))
        .map(|c| if c.is_control() { ' ' } else { c })
        .collect();
    let joined = spaced.split_whitespace().collect::<Vec<_>>().join(" ");
    if joined.chars().count() <= max {
        return joined;
    }
    let mut cut: String = joined.chars().take(max - 1).collect();
    cut.push('…');
    cut
}

#[cfg(test)]
mod tests {
    use super::*;

    fn notice(extra: &str) -> String {
        format!(
            r#"{{"repository":"acme/widgets","pr":7,"clone":"c1","title":"Fix <b>the</b> thing & more"{extra}}}"#
        )
    }

    fn toast(kind: &str, extra: &str) -> Toast {
        let data = notice(extra).replacen('{', &format!(r#"{{"kind":"{kind}","#), 1);
        from_event("notice", &data).expect("a toast")
    }

    #[test]
    fn review_finished_names_the_risk_score() {
        let t = toast("review_finished", r#","risk":4,"risk_status":"complete""#);
        assert_eq!(t.kind, Kind::Finished);
        assert_eq!(t.title, "Review finished: risk 4 of 5");
        // The title is GitHub's, unchanged.
        assert_eq!(t.body, "Fix <b>the</b> thing & more\nacme/widgets #7");
        assert_eq!(
            t.route,
            Route::Pr {
                clone: "c1".into(),
                number: 7
            }
        );
    }

    #[test]
    fn a_lower_bound_score_says_so_and_no_score_is_left_out() {
        let t = toast(
            "review_finished",
            r#","risk":3,"risk_status":"lower_bound""#,
        );
        assert_eq!(t.title, "Review finished: risk at least 3 of 5");
        assert_eq!(toast("review_finished", "").title, "Review finished");
        assert_eq!(
            toast("review_finished", r#","risk":9"#).title,
            "Review finished"
        );
    }

    #[test]
    fn review_failed_names_the_reason() {
        let t = toast("review_failed", r#","reason":"provider_timeout""#);
        assert_eq!(t.kind, Kind::Failed);
        assert_eq!(t.title, "Review failed: provider timeout");
        assert_eq!(toast("review_failed", "").title, "Review failed");
    }

    #[test]
    fn review_requested_and_automatic() {
        let t = toast("review_requested", "");
        assert_eq!(
            (t.kind, t.title.as_str()),
            (Kind::Requested, "Review requested from you")
        );
        let t = toast("review_automatic", r#","risk":2,"risk_status":"complete""#);
        assert_eq!(t.kind, Kind::Automatic);
        assert_eq!(t.title, "Review generated automatically: risk 2 of 5");
    }

    #[test]
    fn a_notice_without_a_clone_opens_the_repository_pull_request() {
        let data = r#"{"kind":"review_requested","repository":"acme/widgets","pr":7}"#;
        let t = from_event("notice", data).unwrap();
        assert_eq!(
            t.route,
            Route::RepositoryPr {
                repository: "acme/widgets".into(),
                number: 7
            }
        );
        assert_eq!(
            t.route
                .url(&Url::parse("http://127.0.0.1:1234/s/ns/").unwrap())
                .query(),
            Some("repository=acme%2Fwidgets&pr=7")
        );
        assert_eq!(t.body, "acme/widgets #7");
    }

    #[test]
    fn text_is_stripped_of_control_characters_and_bounded() {
        let data = serde_json::json!({
            "kind": "review_finished", "repository": "acme/widgets", "pr": 1, "clone": "c",
            "title": format!("a\u{1b}[31m\r\nb\t{}", "x".repeat(500)),
        })
        .to_string();
        let t = from_event("notice", &data).unwrap();
        let title = t.body.lines().next().unwrap();
        assert!(title.starts_with("a [31m b xxx"), "{title}");
        assert_eq!(title.chars().count(), MAX_TITLE);
        assert!(t.body.chars().all(|c| c == '\n' || !c.is_control()));
    }

    #[test]
    fn format_characters_are_stripped() {
        let data = serde_json::json!({
            "kind": "review_finished", "repository": "acme/widgets", "pr": 1, "clone": "c",
            "title": "safe\u{202e}gnp.exe\u{200b}\u{2066}x\u{feff}",
        })
        .to_string();
        let t = from_event("notice", &data).unwrap();
        assert_eq!(t.body.lines().next().unwrap(), "safegnp.exex");
    }

    #[test]
    fn unknown_and_malformed_events_call_for_nothing() {
        assert!(from_event("index", "{}").is_none());
        assert!(from_event("progress", r#"{"job":{}}"#).is_none());
        assert!(from_event("notice", "not json").is_none());
        assert!(from_event("notice", r#"{"kind":"something_new","pr":1}"#).is_none());
        assert!(from_event("notice", r#"{"kind":"review_finished"}"#).is_none());
    }

    fn automatic(extra: &str) -> String {
        format!(
            r#"{{"repository":"Acme/Widgets","enabled":true,"spent_today":5.5,"pending":0{extra}}}"#
        )
    }

    #[test]
    fn the_cap_pause_notifies_only_when_a_cap_is_set() {
        let paused =
            r#","daily_cap":5.0,"paused_until":"2026-10-21T00:00:00Z","notice":"daily_cap""#;
        let t = from_event("automatic", &automatic(paused)).unwrap();
        assert_eq!(t.kind, Kind::CapReached);
        assert_eq!(t.title, "Daily cost cap reached");
        assert_eq!(
            t.body,
            "Automatic reviews for Acme/Widgets are paused. Spent $5.50 of the $5.00 daily cap."
        );
        assert_eq!(t.route, Route::Jobs);
        assert_eq!(
            t.dedupe.as_deref(),
            Some("cap:acme/widgets:2026-10-21T00:00:00Z")
        );
        // No cap, no pause, or another notice: nothing to say.
        for extra in [
            r#","daily_cap":null,"paused_until":"2026-10-21T00:00:00Z","notice":"daily_cap""#,
            r#","daily_cap":5.0,"notice":"daily_cap""#,
            r#","daily_cap":5.0,"paused_until":"2026-10-21T00:00:00Z","notice":"unavailable""#,
            "",
        ] {
            assert!(
                from_event("automatic", &automatic(extra)).is_none(),
                "{extra}"
            );
        }
    }

    #[test]
    fn routes_become_app_urls() {
        let base = Url::parse("http://127.0.0.1:4100/s/abc/").unwrap();
        let pr = Route::Pr {
            clone: "c 1&x".into(),
            number: 7,
        };
        assert_eq!(
            pr.url(&base).as_str(),
            "http://127.0.0.1:4100/s/abc/?clone=c+1%26x&pr=7"
        );
        assert_eq!(
            Route::Jobs.url(&base).as_str(),
            "http://127.0.0.1:4100/s/abc/?page=jobs"
        );
    }

    #[test]
    fn menu_ids_round_trip() {
        for kind in Kind::ALL {
            assert_eq!(Kind::from_menu_id(kind.menu_id()), Some(kind));
        }
        assert_eq!(Kind::from_menu_id("notifications:other"), None);
    }
}

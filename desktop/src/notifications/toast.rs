//! What a daemon event says, as the notification the user sees. Everything
//! here is a pure function of the event: the text comes only from fields the
//! daemon already redacted (the repository, the title as GitHub returned it,
//! the stored risk score, a failure category, an agent session's branch and
//! the masked line its agent showed) and is only stripped of control
//! characters and bounded in length, never reworded.

use serde::Deserialize;
use url::Url;

/// A kind of notification, the unit of the per-kind settings.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Kind {
    Finished,
    Failed,
    Requested,
    Automatic,
    /// The user's own pull request: its checks began failing.
    ChecksFailed,
    /// ... it began conflicting with its base.
    Conflict,
    /// ... a review requested changes.
    ChangesRequested,
    /// ... GitHub would merge it now.
    ReadyToMerge,
    /// A fix pushed a commit to the pull request's branch.
    FixPushed,
    FixFailed,
    /// An agent session asks to be allowed to run a tool.
    SessionPermission,
    /// ... asks the user a question.
    SessionInput,
}

impl Kind {
    pub const ALL: [Kind; 12] = [
        Kind::Finished,
        Kind::Failed,
        Kind::Requested,
        Kind::Automatic,
        Kind::ChecksFailed,
        Kind::Conflict,
        Kind::ChangesRequested,
        Kind::ReadyToMerge,
        Kind::FixPushed,
        Kind::FixFailed,
        Kind::SessionPermission,
        Kind::SessionInput,
    ];

    /// The menu entry's label.
    pub fn label(self) -> &'static str {
        match self {
            Kind::Finished => "Review finished",
            Kind::Failed => "Review failed",
            Kind::Requested => "Review requested from me",
            Kind::Automatic => "Review generated automatically",
            Kind::ChecksFailed => "My pull request: checks failing",
            Kind::Conflict => "My pull request: merge conflict",
            Kind::ChangesRequested => "My pull request: changes requested",
            Kind::ReadyToMerge => "My pull request: ready to merge",
            Kind::FixPushed => "Fix pushed",
            Kind::FixFailed => "Fix failed",
            Kind::SessionPermission => "Agent needs my permission",
            Kind::SessionInput => "Agent waiting for my answer",
        }
    }

    /// The menu entry's identifier.
    pub fn menu_id(self) -> &'static str {
        match self {
            Kind::Finished => "notifications:finished",
            Kind::Failed => "notifications:failed",
            Kind::Requested => "notifications:requested",
            Kind::Automatic => "notifications:automatic",
            Kind::ChecksFailed => "notifications:checks_failed",
            Kind::Conflict => "notifications:conflict",
            Kind::ChangesRequested => "notifications:changes_requested",
            Kind::ReadyToMerge => "notifications:ready_to_merge",
            Kind::FixPushed => "notifications:fix_pushed",
            Kind::FixFailed => "notifications:fix_failed",
            Kind::SessionPermission => "notifications:session_permission",
            Kind::SessionInput => "notifications:session_input",
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
    /// An agent session's transcript, by its `agent:id` key.
    Session { key: String },
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
                Route::Session { key } => {
                    query.append_pair("page", "sessions");
                    query.append_pair("session", key);
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
            Route::Jobs | Route::Session { .. } => None,
        }
    }

    /// The agent session this route shows, if it is one.
    pub fn session(&self) -> Option<&str> {
        match self {
            Route::Session { key } => Some(key),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Toast {
    pub kind: Kind,
    pub title: String,
    pub body: String,
    pub route: Route,
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
    #[serde(default)]
    commit: Option<String>,
    #[serde(default)]
    agent: String,
    #[serde(default)]
    session: String,
    #[serde(default)]
    branch: Option<String>,
    #[serde(default)]
    message: Option<String>,
}

const MAX_TITLE: usize = 200;
const MAX_FIELD: usize = 120;

/// The notification an event calls for, if any. Unknown events, kinds and
/// malformed payloads call for none.
pub fn from_event(name: &str, data: &str) -> Option<Toast> {
    match name {
        "notice" => from_notice(serde_json::from_str(data).ok()?),
        _ => None,
    }
}

fn from_notice(n: Notice) -> Option<Toast> {
    let kind = match n.kind.as_str() {
        "review_finished" => Kind::Finished,
        "review_failed" => Kind::Failed,
        "review_requested" => Kind::Requested,
        "review_automatic" => Kind::Automatic,
        "checks_failed" => Kind::ChecksFailed,
        "conflict" => Kind::Conflict,
        "changes_requested" => Kind::ChangesRequested,
        "ready_to_merge" => Kind::ReadyToMerge,
        "fix_pushed" => Kind::FixPushed,
        "fix_failed" => Kind::FixFailed,
        "session_permission" => Kind::SessionPermission,
        "session_input" => Kind::SessionInput,
        _ => return None,
    };
    if matches!(kind, Kind::SessionPermission | Kind::SessionInput) {
        return from_session_notice(kind, n);
    }
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
        Kind::ChecksFailed => "Checks failing",
        Kind::Conflict => "Merge conflict",
        Kind::ChangesRequested => "Changes requested",
        Kind::ReadyToMerge => "Ready to merge",
        Kind::FixPushed => "Fix pushed",
        Kind::FixFailed => "Fix failed",
        Kind::SessionPermission | Kind::SessionInput => {
            unreachable!("a session notice is read by from_session_notice")
        }
    };
    let reason = n
        .reason
        .as_deref()
        .filter(|r| !r.is_empty())
        .map(|r| clean(&r.replace(['_', '-'], " "), 60));
    let reference = format!("{repository} #{}", n.pr);
    // News about the user's own pull request or a fix leads with what
    // happened and where; its body is the pull request's title. A review's
    // title keeps the outcome and puts the reference under the pull request's
    // title.
    let leads_with_reference = matches!(
        kind,
        Kind::ChecksFailed
            | Kind::Conflict
            | Kind::ChangesRequested
            | Kind::ReadyToMerge
            | Kind::FixPushed
            | Kind::FixFailed
    );
    let title = match (kind, &risk, &reason) {
        (Kind::Finished | Kind::Automatic, Some(risk), _) => format!("{headline}: {risk}"),
        (Kind::Failed, _, Some(reason)) => format!("{headline}: {reason}"),
        (Kind::FixPushed, _, _) => match n.commit.as_deref().map(|c| clean(c, 12)) {
            Some(commit) if !commit.is_empty() => {
                format!("{headline} ({commit}) · {reference}")
            }
            _ => format!("{headline} · {reference}"),
        },
        (Kind::FixFailed, _, Some(reason)) => format!("{headline}: {reason} · {reference}"),
        _ if leads_with_reference => format!("{headline} · {reference}"),
        _ => headline.to_string(),
    };
    let body = match n.title.as_deref().map(|t| clean(t, MAX_TITLE)) {
        Some(t) if !t.is_empty() && leads_with_reference => t,
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
    })
}

/// The agent's name as the app writes it.
fn agent_name(agent: &str) -> String {
    match agent {
        "claude-code" => "Claude Code".into(),
        "codex" => "Codex".into(),
        "cursor" => "Cursor".into(),
        other => clean(other, 32),
    }
}

/// An agent session waiting on the user, like "Claude Code needs your
/// permission · acme/demo" over the line the agent showed and the branch. A
/// click opens the session's transcript. The daemon announces each wait once.
fn from_session_notice(kind: Kind, n: Notice) -> Option<Toast> {
    if n.agent.is_empty() || n.session.is_empty() {
        return None;
    }
    let agent = agent_name(&n.agent);
    let headline = match kind {
        Kind::SessionPermission => format!("{agent} needs your permission"),
        _ => format!("{agent} is waiting for your answer"),
    };
    let repository = clean(&n.repository, MAX_FIELD);
    let title = if repository.is_empty() {
        headline
    } else {
        format!("{headline} · {repository}")
    };
    let message = n
        .message
        .as_deref()
        .map(|m| clean(m, MAX_TITLE))
        .filter(|m| !m.is_empty())
        .unwrap_or_else(|| match kind {
            Kind::SessionPermission => "It asks to run a tool.".into(),
            _ => "It asked you a question.".into(),
        });
    let place = match (n.branch.as_deref().map(|b| clean(b, MAX_FIELD)), n.pr) {
        (Some(branch), pr) if !branch.is_empty() && pr > 0 => format!("{branch} · #{pr}"),
        (Some(branch), _) if !branch.is_empty() => branch,
        (_, pr) if pr > 0 => format!("#{pr}"),
        _ => String::new(),
    };
    let body = if place.is_empty() {
        message
    } else {
        format!("{message}\n{place}")
    };
    Some(Toast {
        kind,
        title,
        body,
        route: Route::Session {
            key: format!("{}:{}", n.agent, n.session),
        },
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
    fn news_about_my_pull_request_leads_with_the_reference() {
        for (name, kind, title) in [
            ("checks_failed", Kind::ChecksFailed, "Checks failing"),
            ("conflict", Kind::Conflict, "Merge conflict"),
            (
                "changes_requested",
                Kind::ChangesRequested,
                "Changes requested",
            ),
            ("ready_to_merge", Kind::ReadyToMerge, "Ready to merge"),
        ] {
            let t = toast(name, "");
            assert_eq!(t.kind, kind);
            assert_eq!(t.title, format!("{title} · acme/widgets #7"));
            assert_eq!(t.body, "Fix <b>the</b> thing & more");
            assert_eq!(
                t.route,
                Route::Pr {
                    clone: "c1".into(),
                    number: 7
                }
            );
        }
        // Without a title the body still names the pull request.
        let data = r#"{"kind":"conflict","repository":"acme/widgets","pr":7}"#;
        assert_eq!(from_event("notice", data).unwrap().body, "acme/widgets #7");
    }

    #[test]
    fn fix_notices_name_the_commit_or_the_reason() {
        let t = toast("fix_pushed", r#","commit":"0123456""#);
        assert_eq!(t.kind, Kind::FixPushed);
        assert_eq!(t.title, "Fix pushed (0123456) · acme/widgets #7");
        assert_eq!(t.body, "Fix <b>the</b> thing & more");
        assert_eq!(
            toast("fix_pushed", "").title,
            "Fix pushed · acme/widgets #7"
        );
        let t = toast("fix_failed", r#","reason":"conflicts_remain""#);
        assert_eq!(t.kind, Kind::FixFailed);
        assert_eq!(t.title, "Fix failed: conflicts remain · acme/widgets #7");
        assert_eq!(
            toast("fix_failed", "").title,
            "Fix failed · acme/widgets #7"
        );
    }

    #[test]
    fn a_waiting_session_names_its_agent_and_opens_its_transcript() {
        let data = r#"{"kind":"session_permission","repository":"acme/demo","pr":7,"agent":"claude-code","session":"s-1","branch":"feature","message":"Claude needs your permission to use Bash"}"#;
        let t = from_event("notice", data).unwrap();
        assert_eq!(t.kind, Kind::SessionPermission);
        assert_eq!(t.title, "Claude Code needs your permission · acme/demo");
        assert_eq!(
            t.body,
            "Claude needs your permission to use Bash\nfeature · #7"
        );
        assert_eq!(
            t.route,
            Route::Session {
                key: "claude-code:s-1".into()
            }
        );
        assert_eq!(t.route.pr(), None);
        assert_eq!(t.route.session(), Some("claude-code:s-1"));
        assert_eq!(
            t.route
                .url(&Url::parse("http://127.0.0.1:1234/s/ns/").unwrap())
                .query(),
            Some("page=sessions&session=claude-code%3As-1")
        );
        // Codex sends no message; a branch without a pull request is named alone.
        let data = r#"{"kind":"session_input","repository":"acme/demo","pr":0,"agent":"codex","session":"c-1","branch":"main"}"#;
        let t = from_event("notice", data).unwrap();
        assert_eq!(t.kind, Kind::SessionInput);
        assert_eq!(t.title, "Codex is waiting for your answer · acme/demo");
        assert_eq!(t.body, "It asked you a question.\nmain");
        let data = r#"{"kind":"session_permission","repository":"acme/demo","pr":0,"agent":"codex","session":"c-1"}"#;
        assert_eq!(
            from_event("notice", data).unwrap().body,
            "It asks to run a tool."
        );
        // Without the session it names, there is nothing to open.
        assert!(
            from_event(
                "notice",
                r#"{"kind":"session_input","repository":"acme/demo","agent":"codex"}"#
            )
            .is_none()
        );
        // The message is cleaned like a title.
        let data = serde_json::json!({
            "kind": "session_permission", "repository": "acme/demo", "agent": "claude-code", "session": "s",
            "message": format!("use\u{1b}[0m\u{202e} Bash {}", "x".repeat(400)),
        })
        .to_string();
        let t = from_event("notice", &data).unwrap();
        assert!(t.body.starts_with("use [0m Bash xx"), "{}", t.body);
        assert_eq!(t.body.chars().count(), MAX_TITLE);
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

    #[test]
    fn an_automatic_status_is_not_a_notification() {
        // An older daemon's daily cap pause is news no more.
        let paused = r#"{"repository":"acme/widgets","enabled":true,"daily_cap":5.0,"spent_today":5.5,"paused_until":"2026-10-21T00:00:00Z","notice":"daily_cap","pending":0}"#;
        assert!(from_event("automatic", paused).is_none());
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

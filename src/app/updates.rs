//! Manual update check. One redirect probe of the latest-release URL, fired
//! only from the tray click: on its own df-hud talks to nothing but the game
//! and the bossmap feed. No API, no JSON - GitHub answers `releases/latest`
//! with a redirect whose Location names the tag.

use std::time::Duration;

pub const LATEST_URL: &str = "https://github.com/xadips/df-hud/releases/latest";
const TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Debug, PartialEq, Eq)]
pub enum Check {
    UpToDate,
    /// The release page is worth opening.
    Newer {
        version: String,
    },
}

pub fn check(current: &str) -> Result<Check, String> {
    check_at(LATEST_URL, current)
}

fn check_at(url: &str, current: &str) -> Result<Check, String> {
    let agent = ureq::Agent::new_with_config(
        ureq::Agent::config_builder()
            .timeout_global(Some(TIMEOUT))
            .http_status_as_error(false)
            .max_redirects(0)
            .build(),
    );
    let resp = agent
        .head(url)
        .header("User-Agent", format!("df-hud/{current}"))
        .call()
        .map_err(|e| e.to_string())?;
    let location = resp
        .headers()
        .get("location")
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| format!("expected a redirect, got HTTP {}", resp.status()))?;
    let latest = tag_version(location)
        .ok_or_else(|| format!("no version tag in redirect to {location:?}"))?;
    if newer(current, &latest) {
        Ok(Check::Newer { version: latest })
    } else {
        Ok(Check::UpToDate)
    }
}

pub fn open_release_page() {
    #[cfg(windows)]
    if let Err(err) = super::autostart::open_file(std::path::Path::new(LATEST_URL)) {
        eprintln!("updates: could not open the release page: {err}");
    }
    #[cfg(not(windows))]
    if let Err(err) = std::process::Command::new("xdg-open")
        .arg(LATEST_URL)
        .spawn()
    {
        eprintln!("updates: could not open the release page: {err}");
    }
}

fn tag_version(location: &str) -> Option<String> {
    let tag = location.rsplit_once("/tag/")?.1;
    let tag = tag.strip_prefix('v').unwrap_or(tag);
    (!tag.is_empty()).then(|| tag.to_string())
}

/// Numeric triples; anything unparseable is "not newer" rather than a scare.
fn newer(current: &str, latest: &str) -> bool {
    match (triple(current), triple(latest)) {
        (Some(cur), Some(new)) => new > cur,
        _ => false,
    }
}

fn triple(v: &str) -> Option<[u64; 3]> {
    let mut fields = v.split('.');
    let mut out = [0u64; 3];
    for slot in &mut out {
        *slot = fields.next()?.parse().ok()?;
    }
    fields.next().is_none().then_some(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Write};
    use std::net::TcpListener;

    #[test]
    fn tag_version_reads_the_redirect() {
        assert_eq!(
            tag_version("https://github.com/xadips/df-hud/releases/tag/v0.4.11").as_deref(),
            Some("0.4.11")
        );
        assert_eq!(
            tag_version("/df-hud/releases/tag/9.9.9").as_deref(),
            Some("9.9.9")
        );
        assert_eq!(
            tag_version("https://github.com/xadips/df-hud/releases"),
            None
        );
        assert_eq!(tag_version("x/tag/"), None);
    }

    #[test]
    fn newer_compares_numerically() {
        assert!(newer("0.4.9", "0.4.10"), "numeric, not lexicographic");
        assert!(newer("0.9.9", "0.10.0"));
        assert!(!newer("0.4.10", "0.4.10"));
        assert!(!newer("0.4.10", "0.4.9"), "a rollback is not an update");
        assert!(!newer("0.4.10", "0.5"), "not a triple");
        assert!(
            !newer("0.4.10", "v0.5.0"),
            "tag_version strips the v, not this"
        );
    }

    fn serve_once(status_line: &str, location: Option<&str>) -> String {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let header = location.map_or(String::new(), |l| format!("Location: {l}\r\n"));
        let status_line = status_line.to_string();
        std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut line = String::new();
            while reader.read_line(&mut line).is_ok() && line != "\r\n" && !line.is_empty() {
                line.clear();
            }
            let _ = write!(
                stream,
                "HTTP/1.1 {status_line}\r\n{header}Content-Length: 0\r\n\r\n"
            );
        });
        format!("http://{addr}/releases/latest")
    }

    #[test]
    fn check_reports_a_newer_release() {
        let url = serve_once("302 Found", Some("/df-hud/releases/tag/v9.9.9"));
        assert_eq!(
            check_at(&url, "0.4.10").unwrap(),
            Check::Newer {
                version: "9.9.9".into()
            }
        );
    }

    #[test]
    fn check_reports_up_to_date() {
        let url = serve_once("302 Found", Some("/df-hud/releases/tag/v0.4.10"));
        assert_eq!(check_at(&url, "0.4.10").unwrap(), Check::UpToDate);
    }

    #[test]
    fn check_without_a_redirect_is_an_error() {
        let url = serve_once("200 OK", None);
        let err = check_at(&url, "0.4.10").unwrap_err();
        assert!(err.contains("redirect"), "{err}");
    }
}

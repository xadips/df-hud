//! Plain ureq GET over a caller-supplied agent. Long-lived clients stay on
//! [`super::dfclient`] / `app`, and the process shares one agent (see
//! `app::df_agent`).

use std::io::Read;
use std::time::Duration;

/// Body of a 200 GET. Timeout and status handling are set on the request, so
/// the agent's own defaults do not matter.
pub fn get_bytes_with(
    agent: &ureq::Agent,
    url: &str,
    user_agent: &str,
    timeout: Duration,
    max_body: u64,
    headers: &[(&str, &str)],
) -> Result<Vec<u8>, String> {
    let mut req = agent
        .get(url)
        .config()
        .timeout_global(Some(timeout))
        .http_status_as_error(false)
        .build()
        .header("User-Agent", user_agent);
    for &(k, v) in headers {
        req = req.header(k, v);
    }
    let resp = req.call().map_err(|e| e.to_string())?;
    if resp.status() != 200 {
        return Err(format!("HTTP {}", resp.status()));
    }
    let mut body = Vec::new();
    resp.into_body()
        .into_reader()
        .take(max_body)
        .read_to_end(&mut body)
        .map_err(|e| format!("reading body: {e}"))?;
    Ok(body)
}

//! One-shot ureq GET. Long-lived clients stay on [`super::dfclient`] / `app`.

use std::io::Read;
use std::time::Duration;

pub fn get_bytes(
    url: &str,
    user_agent: &str,
    timeout: Duration,
    max_body: u64,
    headers: &[(&str, &str)],
) -> Result<Vec<u8>, String> {
    let agent = ureq::AgentBuilder::new().timeout(timeout).build();
    let mut req = agent.get(url).set("User-Agent", user_agent);
    for &(k, v) in headers {
        req = req.set(k, v);
    }
    let resp = req.call().map_err(|e| e.to_string())?;
    if resp.status() != 200 {
        return Err(format!("HTTP {}", resp.status()));
    }
    let mut body = Vec::new();
    resp.into_reader()
        .take(max_body)
        .read_to_end(&mut body)
        .map_err(|e| format!("reading body: {e}"))?;
    Ok(body)
}

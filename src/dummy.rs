//! Dummy View for font/layout tests. Not Derive. Clock is still local.
//!
//! Widget x/y come from defaults (`df-hud.example.toml`).
//! Map is a tiny fake city, not `citymap.txt`.

use crate::scene::{Line, MapCell, MapMarker, MapView, View};

#[cfg(target_os = "linux")]
pub fn clock_hms() -> String {
    let mut t: libc::time_t = 0;
    unsafe { libc::time(&mut t) };
    let mut local = unsafe { std::mem::zeroed::<libc::tm>() };
    unsafe { libc::localtime_r(&t, &mut local) };
    format!(
        "{:02}:{:02}:{:02}",
        local.tm_hour, local.tm_min, local.tm_sec
    )
}

#[cfg(windows)]
pub fn clock_hms() -> String {
    use windows_sys::Win32::Foundation::SYSTEMTIME;
    use windows_sys::Win32::System::SystemInformation::GetLocalTime;
    let mut local = SYSTEMTIME::default();
    unsafe { GetLocalTime(&mut local) };
    format!(
        "{:02}:{:02}:{:02}",
        local.wHour, local.wMinute, local.wSecond
    )
}

pub fn view(clock: &str) -> View {
    View {
        status: "df-hud overlay".into(),
        clock: clock.to_string(),
        xp: "12,345,678".into(),
        xp_color: None,
        block: "Nastya's Holdout".into(),
        block_sub: String::new(),
        challenges: vec![],
        bosses: vec![],
        map: fake_map(),
    }
}

fn fake_map() -> MapView {
    let origin = 1000;
    let size = 17;
    let player = origin + 8;
    let mut cells = Vec::new();
    for y in origin..origin + size {
        for x in origin..origin + size {
            if (x + y) % 11 == 0 && x != player && y != player {
                continue;
            }
            if x == origin + 2 && y % 4 == 0 {
                continue;
            }
            let band = ((x * 3 + y * 5) as i32).rem_euclid(4);
            let (r, g, b) = match band {
                0 => (0x2a, 0x4c, 0x2a),
                1 => (0x3a, 0x5c, 0x34),
                2 => (0x48, 0x64, 0x38),
                _ => (0x24, 0x3c, 0x28),
            };
            cells.push(MapCell {
                x,
                y,
                fill: [r as f32 / 255.0, g as f32 / 255.0, b as f32 / 255.0, 1.0],
            });
        }
    }
    MapView {
        player_x: player,
        player_y: player,
        cells,
        markers: vec![
            MapMarker {
                x: player - 3,
                y: player - 2,
                text: "N".into(),
                ink: [0.75, 1.0, 0.75, 1.0],
                color: [0.75, 1.0, 0.75, 1.0],
                ring: false,
            },
            MapMarker {
                x: player + 4,
                y: player + 1,
                text: "1".into(),
                ink: [1.0, 1.0, 1.0, 1.0],
                color: [1.0, 1.0, 1.0, 1.0],
                ring: false,
            },
        ],
        dividers_x: vec![player],
        dividers_y: vec![player],
        list: vec![
            Line {
                text: "N  Nastya's Holdout".into(),
                ..Line::default()
            },
            Line {
                text: "1  Titan".into(),
                ..Line::default()
            },
        ],
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dummy_view_keeps_the_clock() {
        let clock = clock_hms();
        assert_eq!(clock.len(), 8);
        let v = view(&clock);
        assert_eq!(v.clock, clock);
        assert!(!v.block.is_empty());
    }
}

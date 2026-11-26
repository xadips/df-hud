//! Dummy View for font/layout tests. Not Derive. Clock is still local.
//!
//! Widget x/y come from defaults (`df-hud.example.toml`).
//! Map is a tiny fake city, not `citymap.txt`.

use crate::overlay::scene::{Line, MapCell, MapMarker, MapView, MapWindow, View};

#[cfg(target_os = "linux")]
pub fn clock_hms() -> String {
    let mut t: libc::time_t = 0;
    // SAFETY: `tm` is plain integers plus a `*const c_char`, so all-zero is a
    // valid value (libc gives it no `Default`). `time` and `localtime_r` only
    // write through pointers to the live locals `t` and `local`, and
    // `localtime_r` is the re-entrant form, so no static buffer is shared.
    let local = unsafe {
        let mut local = std::mem::zeroed::<libc::tm>();
        libc::time(&mut t);
        libc::localtime_r(&t, &mut local);
        local
    };
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
    // SAFETY: `local` is a live SYSTEMTIME that GetLocalTime only writes into.
    unsafe { GetLocalTime(&mut local) };
    format!(
        "{:02}:{:02}:{:02}",
        local.wHour, local.wMinute, local.wSecond
    )
}

pub fn view(clock: &str) -> View {
    View {
        status: "df-hud overlay".into(),
        status_color: None,
        clock: clock.to_string(),
        xp: "12,345,678".into(),
        xp_progress: String::new(),
        xp_color: None,
        xp_tallies: vec![],
        block: "Nastya's Holdout".into(),
        block_sub: String::new(),
        challenges: vec![],
        masteries: vec![],
        bosses: vec![],
        boosts: vec![],
        keybinds: vec![
            "G - Minimap".into(),
            "Z - Challenges".into(),
            "J - Overlay".into(),
            "K - Run clock".into(),
            "U - XP/hr".into(),
        ],
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
            let band = (x * 3_i32 + y * 5_i32).rem_euclid(4);
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
        window: MapWindow {
            x: origin,
            y: origin,
            w: size,
            h: size,
        },
        cells,
        markers: vec![
            MapMarker {
                x: player - 3,
                y: player - 2,
                text: "N".into(),
                ink: [0.75, 1.0, 0.75, 1.0],
                color: [0.75, 1.0, 0.75, 1.0],
                ring: false,
                event: false,
            },
            MapMarker {
                x: player + 4,
                y: player + 1,
                text: "1".into(),
                ink: [1.0, 1.0, 1.0, 1.0],
                color: [1.0, 1.0, 1.0, 1.0],
                ring: false,
                event: true,
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

/// Only a map: one event of each kind on the real `citymap.txt` shades, so
/// `marker_color` styles can be judged against them.
#[cfg(all(test, target_os = "linux"))]
pub fn map_showcase() -> View {
    use crate::data::citymap;
    use crate::overlay::present::{
        BANDITS_RGB, BOSS_RGB, DAILY_RGB, MISSION_RGB, NEST_RGB, OUTPOST_RGB, QRF_RGB,
    };

    let origin = 1000;
    let size = 17;
    let player = origin + 8;
    let shade = |letter: u8| {
        citymap::default()
            .shade_of(letter)
            .expect("citymap.txt shade")
            .rgba()
    };
    let districts = [
        [shade(b'c'), shade(b'f'), shade(b'a')],
        [shade(b'i'), shade(b'e'), shade(b'p')],
    ];

    // Nearest first, as the legend sorts. Δ and B5 share a block.
    let events = [
        (9, 13, "Δ", "Wasteland QRF", QRF_RGB, true, "41m 10s"),
        (9, 13, "B5", "Bandits", BANDITS_RGB, false, ""),
        (
            8,
            2,
            "M2",
            "Fort Pastor mission",
            MISSION_RGB,
            true,
            "1h 12m",
        ),
        (3, 12, "DH", "Devil Hound", DAILY_RGB, true, "18m 02s"),
        (2, 3, "N4", "Nest", NEST_RGB, false, "1h 37m"),
        (14, 4, "I2", "Boss", BOSS_RGB, false, ""),
        (14, 11, "B3", "Bandits", BANDITS_RGB, false, ""),
    ];
    let mut markers = vec![MapMarker {
        x: origin + 10,
        y: origin + 10,
        text: "F".into(),
        ink: OUTPOST_RGB,
        color: OUTPOST_RGB,
        ring: false,
        event: false,
    }];
    markers.extend(
        events
            .iter()
            .map(|&(dx, dy, glyph, _, color, ring, _)| MapMarker {
                x: origin + dx,
                y: origin + dy,
                text: glyph.into(),
                ink: [1.0, 1.0, 1.0, 1.0],
                color,
                ring,
                event: true,
            }),
    );
    let list = events
        .iter()
        .map(|&(_, _, glyph, name, color, ring, timer)| Line {
            text: format!("{glyph}  {name}"),
            chip: Some(color),
            chip_ring: ring,
            timer: timer.into(),
            ..Line::default()
        })
        .collect();

    let mut cells = Vec::new();
    for y in origin..origin + size {
        for x in origin..origin + size {
            let marked = markers.iter().any(|m| m.x == x && m.y == y);
            if (x + y) % 11 == 0 && x != player && y != player && !marked {
                continue;
            }
            let col = ((x - origin) / 6).min(2) as usize;
            let row = usize::from(y - origin >= 8);
            cells.push(MapCell {
                x,
                y,
                fill: districts[row][col],
            });
        }
    }

    View {
        map: MapView {
            player_x: player,
            player_y: player,
            window: MapWindow {
                x: origin,
                y: origin,
                w: size,
                h: size,
            },
            cells,
            markers,
            dividers_x: vec![origin + 6, origin + 12],
            dividers_y: vec![origin + 8],
            list,
        },
        ..View::default()
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

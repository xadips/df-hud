//! Embedded city geometry, routing, shading, and location names.

use std::sync::OnceLock;

const RAW: &str = include_str!("../assets/citymap.txt");
const UNREACHABLE: i32 = -1;

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Shade {
    pub letter: u8,
    pub r: u8,
    pub g: u8,
    pub b: u8,
    pub alpha: f64,
}

impl Shade {
    #[cfg(test)]
    pub fn hex(&self) -> String {
        let f = |c: u8| (c as f64 * self.alpha + 0.5) as u8;
        format!("#{:02x}{:02x}{:02x}", f(self.r), f(self.g), f(self.b))
    }
}

#[derive(Clone, Debug)]
pub struct Map {
    pub origin_x: i32,
    pub origin_y: i32,
    pub width: i32,
    pub height: i32,
    pub dividers_x: Vec<i32>,
    pub dividers_y: Vec<i32>,
    cells: Vec<u8>,
    shades: Vec<(u8, Shade)>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Walk {
    pub blocks: i32,
    pub dx: i32,
    pub dy: i32,
    pub detour: i32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Outpost {
    pub x: i32,
    pub y: i32,
    pub name: &'static str,
    pub slug: &'static str,
}

static DEFAULT: OnceLock<Map> = OnceLock::new();

pub fn default() -> &'static Map {
    DEFAULT.get_or_init(|| parse(RAW).expect("citymap.txt"))
}

pub fn parse(s: &str) -> Result<Map, String> {
    let mut m = Map {
        origin_x: 0,
        origin_y: 0,
        width: 0,
        height: 0,
        dividers_x: Vec::new(),
        dividers_y: Vec::new(),
        cells: Vec::new(),
        shades: Vec::new(),
    };
    let mut in_map = false;
    let mut rows: Vec<String> = Vec::new();
    for line in s.lines() {
        if in_map {
            if !line.is_empty() {
                rows.push(line.to_string());
            }
            continue;
        }
        let line = match line.find('#') {
            Some(i) => &line[..i],
            None => line,
        };
        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.is_empty() {
            continue;
        }
        match fields[0] {
            "map" => in_map = true,
            "origin" => {
                if fields.len() != 3 {
                    return Err(format!("origin wants two numbers, got {line:?}"));
                }
                m.origin_x = fields[1].parse().map_err(|e| format!("{e}"))?;
                m.origin_y = fields[2].parse().map_err(|e| format!("{e}"))?;
            }
            "size" => {
                if fields.len() != 3 {
                    return Err(format!("size wants two numbers, got {line:?}"));
                }
                m.width = fields[1].parse().map_err(|e| format!("{e}"))?;
                m.height = fields[2].parse().map_err(|e| format!("{e}"))?;
            }
            "divider" => {
                if fields.len() != 3 {
                    return Err(format!("divider wants an axis and a number, got {line:?}"));
                }
                let n: i32 = fields[2].parse().map_err(|e| format!("{e}"))?;
                match fields[1] {
                    "x" => m.dividers_x.push(n),
                    "y" => m.dividers_y.push(n),
                    other => return Err(format!("divider axis {other:?} is not x or y")),
                }
            }
            "shade" => {
                if fields.len() != 4 || fields[1].len() != 1 {
                    return Err(format!(
                        "shade wants a letter, rrggbb and an alpha, got {line:?}"
                    ));
                }
                let rgb = fields[2];
                if rgb.len() != 6 {
                    return Err(format!("shade {}: bad rgb", fields[1]));
                }
                let r = u8::from_str_radix(&rgb[0..2], 16).map_err(|e| format!("{e}"))?;
                let g = u8::from_str_radix(&rgb[2..4], 16).map_err(|e| format!("{e}"))?;
                let b = u8::from_str_radix(&rgb[4..6], 16).map_err(|e| format!("{e}"))?;
                let a: f64 = fields[3]
                    .parse()
                    .map_err(|e| format!("shade {}: {e}", fields[1]))?;
                let letter = fields[1].as_bytes()[0];
                m.shades.push((
                    letter,
                    Shade {
                        letter,
                        r,
                        g,
                        b,
                        alpha: a,
                    },
                ));
            }
            other => return Err(format!("unknown keyword {other:?}")),
        }
    }
    if m.width <= 0 || m.height <= 0 {
        return Err("size is missing".into());
    }
    if rows.len() as i32 != m.height {
        return Err(format!(
            "map has {} rows, size says {}",
            rows.len(),
            m.height
        ));
    }
    m.cells = Vec::with_capacity((m.width * m.height) as usize);
    for (y, row) in rows.iter().enumerate() {
        if row.len() as i32 != m.width {
            return Err(format!(
                "row {y} is {} wide, size says {}",
                row.len(),
                m.width
            ));
        }
        for (x, c) in row.bytes().enumerate() {
            if c != b'.' && m.shade_of(c).is_none() {
                return Err(format!(
                    "row {y} column {x} uses shade {:?}, which is not declared",
                    c as char
                ));
            }
            m.cells.push(c);
        }
    }
    Ok(m)
}

impl Map {
    fn shade_of(&self, letter: u8) -> Option<Shade> {
        self.shades
            .iter()
            .find(|(l, _)| *l == letter)
            .map(|(_, s)| *s)
    }

    fn index(&self, x: i32, y: i32) -> Option<usize> {
        let cx = x - self.origin_x;
        let cy = y - self.origin_y;
        if cx < 0 || cy < 0 || cx >= self.width || cy >= self.height {
            return None;
        }
        Some((cy * self.width + cx) as usize)
    }

    fn coord_at(&self, i: usize) -> (i32, i32) {
        let i = i as i32;
        (
            self.origin_x + i % self.width,
            self.origin_y + i / self.width,
        )
    }

    pub fn is_block(&self, x: i32, y: i32) -> bool {
        self.index(x, y)
            .map(|i| self.cells[i] != b'.')
            .unwrap_or(false)
    }

    #[cfg(test)]
    pub fn divides_column(&self, x: i32, y: i32) -> bool {
        self.is_block(x - 1, y) || self.is_block(x, y)
    }

    #[cfg(test)]
    pub fn divides_row(&self, x: i32, y: i32) -> bool {
        self.is_block(x, y - 1) || self.is_block(x, y)
    }

    pub fn shade(&self, x: i32, y: i32) -> Option<Shade> {
        let i = self.index(x, y)?;
        let c = self.cells[i];
        if c == b'.' {
            return None;
        }
        self.shade_of(c)
    }

    #[cfg(test)]
    pub fn shades(&self) -> Vec<Shade> {
        let mut out = Vec::new();
        let mut seen = [false; 256];
        for &c in &self.cells {
            if c == b'.' || seen[c as usize] {
                continue;
            }
            seen[c as usize] = true;
            if let Some(s) = self.shade_of(c) {
                out.push(s);
            }
        }
        out.sort_by_key(|s| s.letter);
        out
    }

    #[cfg(test)]
    pub fn blocks(&self) -> i32 {
        self.cells.iter().filter(|c| **c != b'.').count() as i32
    }

    pub fn walk_distances(&self, from_x: i32, from_y: i32) -> Vec<i32> {
        let mut dist = vec![UNREACHABLE; self.cells.len()];
        let Some(start) = self.index(from_x, from_y) else {
            return dist;
        };
        if self.cells[start] == b'.' {
            return dist;
        }
        dist[start] = 0;
        let mut queue = Vec::with_capacity(self.cells.len());
        queue.push(start as i32);
        let mut head = 0;
        while head < queue.len() {
            let i = queue[head] as usize;
            head += 1;
            let (x, y) = self.coord_at(i);
            for (nx, ny) in [(x + 1, y), (x - 1, y), (x, y + 1), (x, y - 1)] {
                let Some(j) = self.index(nx, ny) else {
                    continue;
                };
                if self.cells[j] == b'.' || dist[j] != UNREACHABLE {
                    continue;
                }
                dist[j] = dist[i] + 1;
                queue.push(j as i32);
            }
        }
        dist
    }

    #[cfg(test)]
    pub fn route(&self, from_x: i32, from_y: i32, to_x: i32, to_y: i32) -> Option<Walk> {
        self.route_from(
            &self.walk_distances(from_x, from_y),
            from_x,
            from_y,
            to_x,
            to_y,
        )
    }

    pub fn route_from(
        &self,
        dist: &[i32],
        from_x: i32,
        from_y: i32,
        to_x: i32,
        to_y: i32,
    ) -> Option<Walk> {
        let j = self.index(to_x, to_y)?;
        if j >= dist.len() || dist[j] == UNREACHABLE {
            return None;
        }
        let dx = to_x - from_x;
        let dy = to_y - from_y;
        let straight = dx.abs() + dy.abs();
        Some(Walk {
            blocks: dist[j],
            dx,
            dy,
            detour: dist[j] - straight,
        })
    }
}

pub fn trade_zone_name(zone: i32) -> &'static str {
    match zone {
        1 => "North Western",
        2 => "Northern",
        3 => "North Eastern",
        4 => "Western",
        5 => "Central",
        6 => "Eastern",
        7 => "South Western",
        8 => "Southern",
        9 => "South Eastern",
        10 => "Wastelands",
        21 => "Outpost",
        22 => "Valcrest",
        _ => "",
    }
}

#[cfg(test)]
pub fn trade_zone_short(zone: i32) -> &'static str {
    match zone {
        1 => "NW",
        2 => "North",
        3 => "NE",
        4 => "West",
        5 => "Central",
        6 => "East",
        7 => "SW",
        8 => "South",
        9 => "SE",
        10 => "Wastelands",
        21 => "Outpost",
        22 => "Valcrest",
        _ => "",
    }
}

const OUTPOSTS: &[Outpost] = &[
    Outpost {
        x: 1000,
        y: 1000,
        name: "Nastya's Holdout",
        slug: "nastya",
    },
    Outpost {
        x: 1005,
        y: 985,
        name: "Dogg's Stockade",
        slug: "doggs",
    },
    Outpost {
        x: 1012,
        y: 1019,
        name: "Precinct 13",
        slug: "precinct",
    },
    Outpost {
        x: 1029,
        y: 1003,
        name: "Fort Pastor",
        slug: "fort",
    },
    Outpost {
        x: 1054,
        y: 987,
        name: "Secronom Bunker",
        slug: "bunker",
    },
    Outpost {
        x: 1032,
        y: 985,
        name: "Valcrest",
        slug: "valcrest",
    },
    Outpost {
        x: 1058,
        y: 1019,
        name: "Ground Zero",
        slug: "groundzero",
    },
];

pub fn outposts() -> &'static [Outpost] {
    OUTPOSTS
}

pub fn outpost_name(x: i32, y: i32) -> &'static str {
    OUTPOSTS
        .iter()
        .find(|o| o.x == x && o.y == y)
        .map(|o| o.name)
        .unwrap_or("")
}

pub fn outpost_coords(name: &str) -> Option<(i32, i32)> {
    OUTPOSTS
        .iter()
        .find(|o| o.name == name)
        .map(|o| (o.x, o.y))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;
    use std::path::Path;

    #[test]
    fn default_map_loads() {
        let m = default();
        assert_eq!(
            (m.origin_x, m.origin_y, m.width, m.height),
            (1000, 981, 59, 55)
        );
        assert_eq!(m.blocks(), 1716);
        let shades = m.shades();
        assert_eq!(shades.len(), 16);
        for i in 1..shades.len() {
            assert!(shades[i].letter > shades[i - 1].letter);
        }
        assert_ne!(shades[0].hex(), "#000000");
    }

    #[test]
    fn map_knows_outposts() {
        let m = default();
        for o in outposts() {
            assert!(m.is_block(o.x, o.y), "{} at {},{}", o.name, o.x, o.y);
        }
    }

    #[test]
    fn map_agrees_with_boss_feed() {
        let raw = std::fs::read_to_string(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata/bossmap.json"),
        )
        .unwrap();
        let doc: serde_json::Map<String, Value> = serde_json::from_str(&raw).unwrap();
        let mut checked = 0;
        for (key, raw_event) in &doc {
            let Some(obj) = raw_event.as_object() else {
                continue;
            };
            let Some(locs) = obj.get("locations").and_then(|v| v.as_array()) else {
                continue;
            };
            for loc in locs {
                let Some(pair) = loc.as_array() else {
                    continue;
                };
                if pair.len() != 2 {
                    continue;
                }
                let Ok(x) = pair[0].as_str().unwrap_or("").parse::<i32>() else {
                    continue;
                };
                let Ok(y) = pair[1].as_str().unwrap_or("").parse::<i32>() else {
                    continue;
                };
                if x == 3000 && y == 3000 {
                    continue;
                }
                checked += 1;
                assert!(
                    default().is_block(x, y),
                    "event {key} at {x},{y} is in a gap"
                );
            }
        }
        assert!(checked >= 100, "only checked {checked} feed locations");
    }

    #[test]
    fn gaps_and_connectedness() {
        let m = default();
        assert!(m.is_block(1016, 1020));
        assert!(!m.is_block(1013, 1020));
        assert!(!m.is_block(1058, 1018));
        assert!(!m.is_block(3000, 3000));
        assert!(m.shade(3000, 3000).is_none());
        let dist = m.walk_distances(1000, 1000);
        for (i, cell) in m.cells.iter().enumerate() {
            if *cell != b'.' {
                assert_ne!(dist[i], UNREACHABLE, "{:?} unreachable", m.coord_at(i));
            }
        }
    }

    #[test]
    fn routes() {
        let m = default();
        let walk = m.route(1016, 1021, 1015, 1024).unwrap();
        assert_eq!(walk.blocks, 8);
        assert_ne!(walk.detour, 0);
        let walk = m.route(1046, 999, 1051, 1000).unwrap();
        assert_eq!((walk.blocks, walk.detour, walk.dx, walk.dy), (6, 0, 5, 1));
        for route in [
            [3000, 3000, 1000, 1000],
            [1013, 1020, 1000, 1000],
            [1000, 1000, 1013, 1020],
        ] {
            assert!(m.route(route[0], route[1], route[2], route[3]).is_none());
        }
    }

    #[test]
    fn district_dividers() {
        let m = default();
        assert_eq!(m.dividers_x.len(), 2);
        assert_eq!(m.dividers_y.len(), 3);
        assert!(m.divides_column(1041, 1019));
        assert!(!m.divides_column(1041, 1020));
        assert!(m.divides_row(1035, 1020));
        assert!(!m.divides_row(1005, 1020));
        for y in [985, 995] {
            assert_ne!(m.is_block(1040, y), m.is_block(1041, y));
            assert!(m.divides_column(1041, y));
        }
    }

    #[test]
    fn parse_rejects_nonsense() {
        for (name, body) in [
            ("no size", "origin 1 1\nmap\n..\n"),
            (
                "short row",
                "origin 1 1\nsize 3 1\nshade a 010203 1\nmap\naa\n",
            ),
            (
                "missing rows",
                "origin 1 1\nsize 2 2\nshade a 010203 1\nmap\naa\n",
            ),
            ("unknown shade", "origin 1 1\nsize 2 1\nmap\nab\n"),
            ("unknown key", "origin 1 1\nsize 2 1\nwibble 3\nmap\n..\n"),
            (
                "bad divider",
                "origin 1 1\nsize 2 1\ndivider z 4\nmap\n..\n",
            ),
            (
                "bad shade line",
                "origin 1 1\nsize 2 1\nshade aa 010203 1\nmap\n..\n",
            ),
        ] {
            assert!(parse(body).is_err(), "{name}");
        }
    }

    #[test]
    fn location_names() {
        assert_eq!(trade_zone_name(3), "North Eastern");
        assert_eq!(trade_zone_short(3), "NE");
        assert_eq!(trade_zone_name(99), "");
        assert_eq!(outpost_name(1058, 1019), "Ground Zero");
        assert_eq!(outpost_name(1040, 1000), "");
        assert_eq!(outpost_coords("Secronom Bunker"), Some((1054, 987)));
        assert_eq!(outpost_coords("Loading..."), None);
    }
}

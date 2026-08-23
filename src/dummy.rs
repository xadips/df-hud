//! Hardcoded dummy groups. Not Derive. Screenshot coords are 2560×1440
//! (`df-hud.example.toml` widget x/y). `font_path` waits for Phase 4.

use crate::gpu::TextLine;

const WHITE: [f32; 4] = [1.0, 1.0, 1.0, 1.0];
const HUD_YELLOW: [f32; 4] = [
    0xe6 as f32 / 255.0,
    0xcc as f32 / 255.0,
    0x4d as f32 / 255.0,
    1.0,
];
const BLOCK_BLUE: [f32; 4] = [
    0x9e as f32 / 255.0,
    0xcb as f32 / 255.0,
    0xff as f32 / 255.0,
    1.0,
];

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

pub fn lines(clock: &str) -> [TextLine; 4] {
    [
        TextLine {
            x: 10.0,
            y: 10.0,
            color: HUD_YELLOW,
            text: "df-hud overlay".into(),
        },
        TextLine {
            x: 350.0,
            y: 60.0,
            color: WHITE,
            text: format!("IC Time: {clock}"),
        },
        TextLine {
            x: 220.0,
            y: 85.0,
            color: WHITE,
            text: "Xp/Hr: 12,345,678".into(),
        },
        TextLine {
            x: 2340.0,
            y: 300.0,
            color: BLOCK_BLUE,
            text: "Nastya's Holdout".into(),
        },
    ]
}

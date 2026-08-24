pub mod bossmap;
pub mod catalog;
pub mod challenges;
pub mod citymap;
pub mod xp;

/// Seconds to add to Dead Frontier compact timestamps (`df_servertime`,
/// challenge start/end) to get Unix time. Live: `df_servertime = 586484051`
/// at unix `1786484051`.
pub const TIME_OFFSET: i64 = 1_200_000_000;

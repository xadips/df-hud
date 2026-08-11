package main

// Location naming. Everything here is a port of the game's own tables, not a
// guess, and each one names its source so it can be re-checked when the game
// changes.

// tradeZoneName ports tradezoneNamer (base.js:2017+). Values 1..9 are a 3x3
// division of the city, and since the block grid is 39x39, each trade zone
// covers 13x13 blocks - which is why this is usable for Block Info with no
// coordinate calibration at all.
func tradeZoneName(zone int) string {
	switch zone {
	case 1:
		return "North Western"
	case 2:
		return "Northern"
	case 3:
		return "North Eastern"
	case 4:
		return "Western"
	case 5:
		return "Central"
	case 6:
		return "Eastern"
	case 7:
		return "South Western"
	case 8:
		return "Southern"
	case 9:
		return "South Eastern"
	case 10:
		return "Wastelands"
	case 21:
		return "Outpost"
	case 22:
		return "Valcrest"
	}
	return ""
}

// tradeZoneShort ports tradezoneNamerShort, used where space is tight - which on
// a HUD is most places.
func tradeZoneShort(zone int) string {
	switch zone {
	case 1:
		return "NW"
	case 2:
		return "North"
	case 3:
		return "NE"
	case 4:
		return "West"
	case 5:
		return "Central"
	case 6:
		return "East"
	case 7:
		return "SW"
	case 8:
		return "South"
	case 9:
		return "SE"
	case 10:
		return "Wastelands"
	case 21:
		return "Outpost"
	case 22:
		return "Valcrest"
	}
	return ""
}

// outpost is one of the seven fixed outposts.
type outpost struct {
	X, Y int
	Name string
	// Slug is the game's own asset name, kept because it is the identifier the
	// game uses and the only thing the source actually proves.
	Slug string
}

// outposts is the coordinate table from initOutpost (newoutpost.js:13-36),
// paired with the JSON asset each type loads (newoutpost.js:97-124). The
// coordinates and slugs are straight from that code; the display names are the
// game's public names for those outposts.
//
// Independently cross-checked: DFProfiler reports the captured account's outpost
// as "Ground Zero" while its df_positionx/y read 1058,1019 - which is exactly
// the type-7 entry below. So this table is right, and position does equal the
// outpost coordinates while you are standing in one.
var outposts = []outpost{
	{1000, 1000, "Nastya's Holdout", "nastya"},
	{1005, 985, "Dogg's Stockade", "doggs"},
	{1012, 1019, "Precinct 13", "precinct"},
	{1029, 1003, "Fort Pastor", "fort"},
	{1054, 987, "Secronom Bunker", "bunker"},
	{1032, 985, "Valcrest", "valcrest"},
	{1058, 1019, "Ground Zero", "groundzero"},
}

// outpostName returns the outpost at these coordinates, or "" if they are not an
// outpost. Note that a position matching an outpost does NOT by itself mean you
// are inside it: df_inoutpost is the authority for that, and this only names the
// place.
func outpostName(x, y int) string {
	for _, o := range outposts {
		if o.X == x && o.Y == y {
			return o.Name
		}
	}
	return ""
}

// SPDX-License-Identifier: GPL-3.0-or-later
package catalog

// Builtin is a plugin shipped in the matched S2Script release.
type Builtin struct {
	Name           string
	DefaultEnabled bool
	Variable       string
}

// Bundled returns a fresh copy of the deliberately reviewed release catalog.
func Bundled() []Builtin {
	return []Builtin{
		{"@s2script/basecommands", true, "S2SCRIPT_PLUGIN_BASECOMMANDS"},
		{"@s2script/basechat", true, "S2SCRIPT_PLUGIN_BASECHAT"},
		{"@s2script/playercommands", true, "S2SCRIPT_PLUGIN_PLAYERCOMMANDS"},
		{"@s2script/antiflood", true, "S2SCRIPT_PLUGIN_ANTIFLOOD"},
		{"@s2script/adminhelp", true, "S2SCRIPT_PLUGIN_ADMINHELP"},
		{"@s2script/basecomm", true, "S2SCRIPT_PLUGIN_BASECOMM"},
		{"@s2script/basebans", true, "S2SCRIPT_PLUGIN_BASEBANS"},
		{"@s2script/reservedslots", true, "S2SCRIPT_PLUGIN_RESERVEDSLOTS"},
		{"@s2script/basetriggers", true, "S2SCRIPT_PLUGIN_BASETRIGGERS"},
		{"@s2script/funcommands", true, "S2SCRIPT_PLUGIN_FUNCOMMANDS"},
		{"@s2script/clientprefs", true, "S2SCRIPT_PLUGIN_CLIENTPREFS"},
		{"@s2script/adminmenu", true, "S2SCRIPT_PLUGIN_ADMINMENU"},
		{"@s2script/basevotes", true, "S2SCRIPT_PLUGIN_BASEVOTES"},
		{"@s2script/zones", true, "S2SCRIPT_PLUGIN_ZONES"},
		{"@s2script/nominations", false, "S2SCRIPT_PLUGIN_NOMINATIONS"},
		{"@s2script/rockthevote", false, "S2SCRIPT_PLUGIN_ROCKTHEVOTE"},
		{"@s2script/nextmap", false, "S2SCRIPT_PLUGIN_NEXTMAP"},
		{"@s2script/funvotes", false, "S2SCRIPT_PLUGIN_FUNVOTES"},
	}
}

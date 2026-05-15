package handlers

import "testing"

func TestIsDiscordHost(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"discord.com", true},
		{"discord.com:443", true},
		{"DISCORD.COM", true},
		{"discord.com.", true},
		{"discord.gg", true},
		{"gateway.discord.gg", true},
		{"foo.discord.gg", true},
		{"a.b.discord.com", true},
		{"evil-discord.com", false},
		{"evil-discord.com.attacker.io", false},
		{"discord.com.attacker.io", false},
		{"notdiscord.com", false},
		{"", false},
		{"localhost:8080", false},
		{"example.com", false},
	}
	for _, tc := range cases {
		got := isDiscordHost(tc.in)
		if got != tc.want {
			t.Errorf("isDiscordHost(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

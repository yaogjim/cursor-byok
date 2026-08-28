package appearance

import "strings"

const (
	Light   = "light"
	Dark    = "dark"
	System  = "system"
	Default = Light
)

func Normalize(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", Light:
		return Light
	case Dark:
		return Dark
	case System:
		return System
	default:
		return Default
	}
}

var systemPrefersDark = detectSystemPrefersDark

func Resolve(preferred string) string {
	switch Normalize(preferred) {
	case Dark:
		return Dark
	case System:
		if systemPrefersDark() {
			return Dark
		}
		return Light
	default:
		return Light
	}
}

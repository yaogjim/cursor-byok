package updater

import (
	"strconv"
	"strings"
)

type semanticVersion struct {
	parts      []int
	prerelease string
}

func compareVersions(a, b string) int {
	left := parseVersion(a)
	right := parseVersion(b)
	maxParts := len(left.parts)
	if len(right.parts) > maxParts {
		maxParts = len(right.parts)
	}

	for i := 0; i < maxParts; i++ {
		if cmp := compareInts(versionPart(left.parts, i), versionPart(right.parts, i)); cmp != 0 {
			return cmp
		}
	}

	if left.prerelease == right.prerelease {
		return 0
	}
	if left.prerelease == "" {
		return 1
	}
	if right.prerelease == "" {
		return -1
	}
	if left.prerelease > right.prerelease {
		return 1
	}
	if left.prerelease < right.prerelease {
		return -1
	}
	return 0
}

func parseVersion(raw string) semanticVersion {
	clean := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	if clean == "" {
		return semanticVersion{}
	}

	var prerelease string
	if idx := strings.Index(clean, "-"); idx >= 0 {
		prerelease = clean[idx+1:]
		clean = clean[:idx]
	}

	rawParts := strings.Split(clean, ".")
	parts := make([]int, 0, len(rawParts))
	for _, part := range rawParts {
		parts = append(parts, atoi(part))
	}
	return semanticVersion{parts: parts, prerelease: prerelease}
}

func versionPart(parts []int, index int) int {
	if index < len(parts) {
		return parts[index]
	}
	return 0
}

func atoi(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}

func compareInts(a, b int) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	default:
		return 0
	}
}

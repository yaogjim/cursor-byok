package runtimecore

// CanonicalToolName maps the four Shell/Bash spellings onto the catalog name.
// Other names are returned unchanged; callers trim before invoking this helper.
func CanonicalToolName(name string) string {
	switch name {
	case "Shell", "shell", "Bash", "bash":
		return "Shell"
	default:
		return name
	}
}

package mode

// TemplateKey returns the registry key for a mode template.
// Version is ignored — mode templates are keyed by name only.
func TemplateKey(name, version string) string {
	return name
}

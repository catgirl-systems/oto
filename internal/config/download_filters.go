package config

// Defaults are intentionally small, and filtering remains opt-in.
func DefaultDownloadFilters() []string {
	return []string{"*.DS_Store", "*.exe", "*.msi", "desktop.ini", "Thumbs.db"}
}

func NormalizeDownloadFilters(rules []string) ([]string, error) {
	if rules == nil {
		rules = DefaultDownloadFilters()
	}
	return NormalizeShareExclusions(rules)
}

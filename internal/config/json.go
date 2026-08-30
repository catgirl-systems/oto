package config

// SaveJSON writes a private JSON state file using the same atomic guarantees as Config.Save.
func SaveJSON(path string, value any) error { return atomicJSON(path, value, 0600) }

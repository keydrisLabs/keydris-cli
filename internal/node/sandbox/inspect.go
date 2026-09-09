package sandbox

// HasKeydrisHooks distinguishes an unused tool from a damaged Keydris setup.
func HasKeydrisHooks(path string) (bool, error) {
	settings, err := readSettings(path)
	if err != nil {
		return false, err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "SessionEnd", "PreToolUse", "PermissionRequest"} {
		entries, _ := hooks[event].([]any)
		for _, entry := range entries {
			if entryReferencesKeydris(entry) {
				return true, nil
			}
		}
	}
	return false, nil
}

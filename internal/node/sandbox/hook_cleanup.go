package sandbox

// stripKeydrisHandlers keeps other handlers even when a user placed them in
// the same matcher group as a Keydris command.
func stripKeydrisHandlers(entry any) (any, bool) {
	group, ok := entry.(map[string]any)
	if !ok {
		return entry, false
	}
	handlers, ok := group["hooks"].([]any)
	if !ok {
		return entry, false
	}
	kept := make([]any, 0, len(handlers))
	changed := false
	for _, handler := range handlers {
		hook, _ := handler.(map[string]any)
		command, _ := hook["command"].(string)
		if isKeydrisCommand(command) {
			changed = true
			continue
		}
		kept = append(kept, handler)
	}
	if !changed {
		return entry, false
	}
	if len(kept) == 0 {
		return nil, true
	}
	group["hooks"] = kept
	return group, true
}

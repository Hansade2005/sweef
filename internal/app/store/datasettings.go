package store

// Budget returns the validated data.budget setting.
func (s *Settings) Budget() Budget {
	b, _ := ParseBudget(s.Get("data.budget"))
	return b
}

// DataSites returns a copy of the per-origin Data Saver rules
// (origin -> "full" | "lite").
func (s *Settings) DataSites() map[string]string {
	out := map[string]string{}
	if m, ok := s.Get("data.sites").(map[string]any); ok {
		for k, v := range m {
			if r, _ := v.(string); r != "" {
				out[k] = r
			}
		}
	}
	return out
}

// DataLevel resolves data.level: an explicit choice wins; "auto" is standard
// when a budget is set and off otherwise.
func (s *Settings) DataLevel() string {
	switch l := s.Str("data.level"); l {
	case "off", "standard", "strict":
		return l
	}
	if s.Budget().Enabled {
		return "standard"
	}
	return "off"
}

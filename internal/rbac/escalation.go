package rbac

// Covers reports whether the effective permissions held cover every effective permission of
// want (R-AUTH-152). A wanted permission is expanded to the catalogue methods it names; each of
// them must be allowed by held on the wanted project ("*" only by a held "*" permission). A
// wanted method the catalogue does not know is never covered.
func (c *Catalog) Covers(held, want []Permission) bool {
	for _, w := range want {
		ms := c.Expand(w.Method)
		if len(ms) == 0 {
			return false
		}
		for _, m := range ms {
			if !Allows(held, m, w.Project) {
				return false
			}
		}
	}
	return true
}

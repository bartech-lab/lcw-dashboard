package scheduler

import "github.com/bartech/lcw-dashboard/internal/lcw"

func sortableFields() []string {
	fields := lcw.ValidSortFields()
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, string(f))
	}
	return out
}

func deltaWindows() []string {
	windows := lcw.ValidWindows()
	out := make([]string, 0, len(windows))
	for _, w := range windows {
		out = append(out, string(w))
	}
	return out
}

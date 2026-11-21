// Package level defines the ordered notification levels.
package level

const (
	Passive       = "passive"
	Active        = "active"
	TimeSensitive = "time-sensitive"
	Critical      = "critical"
	Page          = "page"
)

// Ordered lists the levels from lowest to highest.
var Ordered = []string{Passive, Active, TimeSensitive, Critical, Page}

// Rank returns the position of l in the order, or -1 if l is not a level.
func Rank(l string) int {
	for i, v := range Ordered {
		if v == l {
			return i
		}
	}
	return -1
}

// Valid reports whether l is a known level.
func Valid(l string) bool { return Rank(l) >= 0 }

// Min returns the lower of two valid levels.
func Min(a, b string) string {
	if Rank(a) <= Rank(b) {
		return a
	}
	return b
}

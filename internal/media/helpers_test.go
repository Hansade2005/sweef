package media

import (
	"fmt"
	"strings"
)

func sscanRatio(s string, a, b *float64) (int, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("bad ratio")
	}
	return fmt.Sscanf(parts[0]+" "+parts[1], "%g %g", a, b)
}

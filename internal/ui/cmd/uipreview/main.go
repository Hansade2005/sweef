// Command uipreview renders every Sweef UI page through the real engine and
// writes PNG previews. Usage: go run ./internal/ui/cmd/uipreview -out docs/design
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sweefbrowser/sweef/internal/ui/preview"
)

func main() {
	out := flag.String("out", "docs/design", "output directory")
	filter := flag.String("only", "", "only scenarios whose name contains this")
	scales := flag.String("scales", "1,2", "comma-separated device scale factors")
	flag.Parse()
	var sf []float64
	for _, s := range strings.Split(*scales, ",") {
		var f float64
		if _, err := fmt.Sscan(s, &f); err == nil && f > 0 {
			sf = append(sf, f)
		}
	}
	files, err := preview.Generate(*out, *filter, sf, func(s string) { fmt.Println(s) })
	fmt.Printf("wrote %d files to %s\n", len(files), *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

package engine

import (
	"fmt"
	"os"
	"testing"
)

func TestZZSkipReasons(t *testing.T) {
	u := os.Getenv("SWEEF_DBG_URL")
	if u == "" {
		t.Skip()
	}
	e := New(DefaultOptions())
	defer e.Close()
	c := e.NewContext("", ContextOptions{})
	c.SetDataPolicy(PolicyFor(SaverStandard))
	tab, _ := c.NewTab("about:blank")
	_ = tab.Navigate(u, nil)
	_, _ = tab.PaintRGBA(1280, 720, 1, false)
	for _, it := range tab.Skipped() {
		fmt.Println(it.Kind, it.Reason, it.Bytes, it.URL)
	}
	if env := tab.ds.env.Load(); env != nil {
		if set := env.fonts.webSet(); set != nil {
			for _, f := range set.Faces() {
				fmt.Println(f.Desc.Family, f.Desc.WMin, f.Desc.WMax, f.State(), f.Err(), len(f.Desc.Src), f.Desc.Src[0].URL, f.Desc.Src[0].Format)
			}
		}
	}
}

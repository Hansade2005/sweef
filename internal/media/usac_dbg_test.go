package media

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestUSACDbgConfig(t *testing.T) {
	dir := os.Getenv("SWEEF_FATE_DIR")
	if dir == "" {
		t.Skip()
	}
	files, _ := filepath.Glob(filepath.Join(dir, "usac", "*.m*"))
	for _, f := range files {
		data, _ := os.ReadFile(f)
		info, err := ProbeBytes(data)
		if err != nil {
			t.Log(f, err)
			continue
		}
		as, err := loadAudioStream(data, info)
		if err != nil {
			t.Log(f, err)
			continue
		}
		fmt.Printf("%s: codec=%s packets=%d skip=%d editDur=%d cfg=% x\n", filepath.Base(f), info.Audio().Codec, len(as.packets), as.skip, as.editDur, as.cfg)
		c, err := parseUsacASC(as.cfg)
		if err != nil {
			fmt.Println("  err", err)
			continue
		}
		fmt.Printf("  %+v\n", *c)
	}
}

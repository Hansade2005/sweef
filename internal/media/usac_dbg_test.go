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

// TestUSACDbgDump writes libxaac "raw" test inputs (scratch use only).
func TestUSACDbgDump(t *testing.T) {
	dir := os.Getenv("SWEEF_FATE_DIR")
	out := os.Getenv("USAC_DUMP_DIR")
	if dir == "" || out == "" {
		t.Skip()
	}
	files, _ := filepath.Glob(filepath.Join(dir, "usac", "*.m*"))
	more, _ := filepath.Glob(filepath.Join(dir, "aac", "*.mp4"))
	files = append(files, more...)
	for _, f := range files {
		data, _ := os.ReadFile(f)
		info, err := ProbeBytes(data)
		if err != nil || info.Audio() == nil || info.Audio().Codec != "mp4a.40.42" {
			continue
		}
		as, err := loadAudioStream(data, info)
		if err != nil {
			continue
		}
		base := filepath.Join(out, filepath.Base(f))
		raw := append([]byte{}, as.cfg...)
		for _, p := range as.packets {
			raw = append(raw, p...)
		}
		os.WriteFile(base+".raw", raw, 0o644)
		m := fmt.Sprintf("-dec_info_init:%d\n-g_track_count:1\n-movie_time_scale:48000\n-media_time_scale:48000\n-ia_mp4_stsz_entries:%d\n-playTimeInSamples:%d\n-startOffsetInSamples:%d\n-useEditlist:1\n", len(as.cfg), len(as.packets), as.editDur, as.skip)
		for _, p := range as.packets {
			m += fmt.Sprintf("-ia_mp4_stsz_size:%d\n", len(p))
		}
		os.WriteFile(base+".meta", []byte(m), 0o644)
	}
}

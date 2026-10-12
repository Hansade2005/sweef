package hevc

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConformance decodes JCT-VC conformance bitstreams found below the
// directory named by HEVC_CONF (*.bit / *.bin files, searched recursively)
// and checks every picture against the decoded picture hash SEI of the
// stream and the whole output against the .md5 file shipped with it. The
// streams are not part of the repository; the test is skipped without them.
func TestConformance(t *testing.T) {
	root := os.Getenv("HEVC_CONF")
	if root == "" {
		t.Skip("HEVC_CONF not set")
	}
	filter := os.Getenv("HEVC_CONF_RUN")
	var files []string
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && (strings.HasSuffix(p, ".bit") || strings.HasSuffix(p, ".bin")) {
			if filter == "" || strings.Contains(p, filter) {
				files = append(files, p)
			}
		}
		return nil
	})
	for _, f := range files {
		f := f
		t.Run(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			d := NewDecoder()
			d.CheckHash = true
			frames, derr := d.Push(data)
			frames = append(frames, d.Flush()...)
			chk, bad := d.HashStats()
			h := md5.New()
			for _, fr := range frames {
				h.Write(frameBytes(fr))
			}
			sum := hex.EncodeToString(h.Sum(nil))
			status := "no md5 file"
			dir := filepath.Dir(f)
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if !strings.Contains(strings.ToLower(e.Name()), "md5") {
					continue
				}
				if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
					if status == "no md5 file" {
						status = "yuv md5 differs"
					}
					if strings.Contains(strings.ToLower(string(b)), sum) {
						status = "yuv md5 OK"
					}
				}
			}
			t.Logf("frames=%d hashes checked=%d bad=%d err=%v %s", len(frames), chk, bad, derr, status)
			if derr != nil || bad != 0 || (chk == 0 && status != "yuv md5 OK") {
				t.Fail()
			}
		})
	}
}

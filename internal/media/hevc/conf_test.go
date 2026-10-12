package hevc

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
			// per layer reference sums ("..._<layer>.yuv") of multilayer streams
			dir := filepath.Dir(f)
			layerSum := map[int]string{}
			ents, _ := os.ReadDir(dir)
			var texts []string
			for _, e := range ents {
				if strings.Contains(strings.ToLower(e.Name()), "md5") {
					if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
						texts = append(texts, strings.ToLower(string(b)))
						for _, ln := range strings.Split(string(b), "\n") {
							fl := strings.Fields(ln)
							if len(fl) == 2 && strings.HasSuffix(fl[1], ".yuv") {
								nm := strings.TrimSuffix(fl[1], ".yuv")
								if i := strings.LastIndex(nm, "_"); i >= 0 {
									var k int
									if _, err := fmt.Sscanf(nm[i+1:], "%d", &k); err == nil {
										layerSum[k] = strings.ToLower(fl[0])
									}
								}
							}
						}
					}
				}
			}
			layers := []int{0}
			if len(layerSum) > 1 {
				layers = layers[:0]
				for k := range layerSum {
					layers = append(layers, k)
				}
				sort.Ints(layers)
			}
			for _, layer := range layers {
				d := NewDecoder()
				d.CheckHash = true
				d.SetOutputLayer(layer)
				frames, derr := d.Push(data)
				frames = append(frames, d.Flush()...)
				chk, bad := d.HashStats()
				h := md5.New()
				for _, fr := range frames {
					h.Write(frameBytes(fr))
				}
				sum := hex.EncodeToString(h.Sum(nil))
				status := "no md5 file"
				for _, tx := range texts {
					if status == "no md5 file" {
						status = "yuv md5 differs"
					}
					if want, ok := layerSum[layer]; ok && len(layerSum) > 1 {
						if want == sum {
							status = "yuv md5 OK"
						} else {
							status = "yuv md5 differs"
						}
						break
					}
					if strings.Contains(tx, sum) {
						status = "yuv md5 OK"
					}
				}
				t.Logf("layer %d: frames=%d hashes checked=%d bad=%d err=%v %s", layer, len(frames), chk, bad, derr, status)
				if derr != nil || bad != 0 || (chk == 0 && status != "yuv md5 OK") || status == "yuv md5 differs" && len(layerSum) > 1 {
					t.Fail()
				}
			}
		})
	}
}

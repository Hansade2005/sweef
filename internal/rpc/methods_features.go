package rpc

// methods_features.go — PDF, downloads, exposed bindings and route rules.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/pdf"
)

func init() {
	registerExt("Page.pdf", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		o := pdf.DefaultOptions()
		if v := num(p, "width"); v > 0 {
			o.PageWidthPt = v
		}
		if v := num(p, "height"); v > 0 {
			o.PageHeightPt = v
		}
		if v := num(p, "margin"); v > 0 {
			o.MarginPt = v
		}
		if v := num(p, "scale"); v > 0 {
			o.BaseFontPt *= v
		}
		if p["landscape"] == true {
			o.Landscape = true
		}
		if v, ok := p["displayHeaderFooter"].(bool); ok {
			o.HeaderFooter = v
		}
		switch str(p, "format") {
		case "Letter", "letter":
			o.PageWidthPt, o.PageHeightPt = 612, 792
		case "Legal", "legal":
			o.PageWidthPt, o.PageHeightPt = 612, 1008
		case "A3", "a3":
			o.PageWidthPt, o.PageHeightPt = 841.89, 1190.55
		case "A5", "a5":
			o.PageWidthPt, o.PageHeightPt = 419.53, 595.28
		}
		b, err := t.PDF(o)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"bytes": len(b)}
		if path := str(p, "path"); path != "" {
			if err := os.WriteFile(path, b, 0o644); err != nil {
				return nil, err
			}
			out["path"] = path
		} else {
			out["pdfBase64"] = base64.StdEncoding.EncodeToString(b)
		}
		return out, nil
	})

	registerExt("Page.downloads", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"downloads": t.Downloads()}, nil
	})
	registerExt("Page.download", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		d, err := t.DownloadURL(str(p, "url"), str(p, "filename"))
		if err != nil {
			return nil, err
		}
		return d, nil
	})

	registerExt("Runtime.bindingCalls", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"calls": t.BindingCalls(int64(num(p, "since")))}, nil
	})
	registerExt("Runtime.setBindingReply", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		reply, err := replyJSON(p["reply"])
		if err != nil {
			return nil, err
		}
		t.SetBindingReply(str(p, "name"), reply)
		return map[string]bool{"ok": true}, nil
	})

	registerExt("Network.routes", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"routes": t.Routes()}, nil
	})
}

// replyJSON turns a binding reply given as any JSON value (or a string that
// already holds JSON) into the JSON text page code receives.
func replyJSON(v any) (string, error) {
	if v == nil {
		return "null", nil
	}
	if s, ok := v.(string); ok && json.Valid([]byte(s)) {
		return s, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("reply: %w", err)
	}
	return string(b), nil
}

// routeFromParams builds a RouteRule from Network.route parameters.
func routeFromParams(p Params) *engine.RouteRule {
	r := &engine.RouteRule{
		Pattern:      str(p, "pattern"),
		Method:       str(p, "method"),
		BodyContains: str(p, "bodyContains"),
		Action:       strOr(p, "action", "fulfill"),
		Status:       int(num(p, "status")),
		Body:         str(p, "body"),
		ContentType:  str(p, "contentType"),
		URL:          str(p, "url"),
		NewMethod:    str(p, "newMethod"),
		Times:        int(num(p, "times")),
	}
	if nb, ok := p["newBody"].(string); ok {
		r.NewBody = &nb
	}
	if r.Body == "" {
		if j, ok := p["json"]; ok {
			b, _ := json.Marshal(j)
			r.Body = string(b)
			if r.ContentType == "" {
				r.ContentType = "application/json"
			}
		}
	}
	if hm, ok := p["headers"].(map[string]any); ok {
		r.Headers = map[string]string{}
		for k, v := range hm {
			r.Headers[k] = fmt.Sprint(v)
		}
	}
	return r
}

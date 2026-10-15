package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// tools.go - the tool set (deliberately small: six tools), its byte-stable
// serialization and input validation.
//
// Stability: the definitions are hand-written JSON constants (key order is
// fixed by the source text), tools are sorted by name, and the serialized
// array is built once at init. Nothing is derived from maps, time or
// randomness, so the tools block of the request is byte-identical on every
// turn of every run. Every tool has eager_input_streaming:true so tool input
// streams as it is generated; the consequence is that the streamed JSON may be
// truncated or invalid, which is why every handler validates its input and
// answers with an is_error tool_result instead of acting on guesses.

const (
	toolAct      = "act"
	toolTabs     = "tabs"
	toolReadPage = "read_page"
	toolFind     = "find"
	toolAskUser  = "ask_user"
	toolDone     = "done"
)

var toolDefs = map[string]string{
	toolAct:      `{"name":"act","description":"Run ordered browser steps in one round trip; stops at the first failing step; returns per-step results and one observation of the final page (full snapshot on a new page, delta otherwise).","input_schema":{"type":"object","properties":{"steps":{"type":"array","minItems":1,"items":{"type":"object","properties":{"op":{"type":"string","enum":["navigate","click","hover","type","press","scroll","wait","select","back","forward","extract","screenshot","expect"],"description":"navigate{url} click{ref} hover{ref} type{ref,text,submit?,append?} press{key,ref?} scroll{dir?,amount?} wait{ms|text|url} select{ref,value} back forward extract{ref?}=text of element or page screenshot=only if the snapshot cannot answer expect{kind,value,ref?}=assert"},"ref":{"type":"string","description":"e.g. e41"},"url":{"type":"string","description":"navigate: absolute URL; wait: URL glob"},"text":{"type":"string"},"key":{"type":"string","description":"e.g. Enter, Control+a"},"dir":{"type":"string","enum":["up","down"]},"amount":{"type":"integer"},"ms":{"type":"integer","description":"max 10000"},"value":{"type":"string"},"kind":{"type":"string","enum":["text","url","title","visible","hidden","count","value"]},"submit":{"type":"boolean","description":"type: press Enter after"},"append":{"type":"boolean"},"optional":{"type":"boolean","description":"failure does not stop the batch"}},"required":["op"]}},"observe":{"type":"string","enum":["auto","none","full"]},"tab":{"type":"string"},"note":{"type":"string","description":"task memory (long tasks only)"}},"required":["steps"]},"eager_input_streaming":true}`,
	toolTabs:     `{"name":"tabs","description":"List, open, switch to or close tabs. open and switch return a snapshot and make that tab current. Close only tabs you opened.","input_schema":{"type":"object","properties":{"action":{"type":"string","enum":["list","open","switch","close"]},"url":{"type":"string"},"id":{"type":"string"}},"required":["action"]},"eager_input_streaming":true}`,
	toolReadPage: `{"name":"read_page","description":"Snapshot of a tab (delta if you hold a current one; mode full forces the whole page). Not needed after act.","input_schema":{"type":"object","properties":{"tab":{"type":"string"},"mode":{"type":"string","enum":["auto","full"]}}},"eager_input_streaming":true}`,
	toolFind:     `{"name":"find","description":"Return only the snapshot lines (with refs) matching text and/or role; cheaper than re-reading a long page.","input_schema":{"type":"object","properties":{"query":{"type":"string"},"role":{"type":"string"},"tab":{"type":"string"},"limit":{"type":"integer","description":"default 20, max 60"}}},"eager_input_streaming":true}`,
	toolAskUser:  `{"name":"ask_user","description":"Ask the user one short question when you cannot proceed without them (ambiguous goal, missing credential, their decision, captcha).","input_schema":{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}}},"required":["question"]},"eager_input_streaming":true}`,
	toolDone:     `{"name":"done","description":"Finish. summary: the result first, then the facts; success false if not completed.","input_schema":{"type":"object","properties":{"summary":{"type":"string"},"success":{"type":"boolean"}},"required":["summary"]},"eager_input_streaming":true}`,
}

// toolsJSON is the serialized tools array without cache_control;
// toolsJSONCached has the breakpoint on the last tool.
var toolsJSON, toolsJSONCached []byte

func init() {
	names := make([]string, 0, len(toolDefs))
	for n := range toolDefs {
		names = append(names, n)
	}
	sort.Strings(names)
	var plain, cached bytes.Buffer
	plain.WriteByte('[')
	cached.WriteByte('[')
	for i, n := range names {
		var cb bytes.Buffer
		if err := json.Compact(&cb, []byte(toolDefs[n])); err != nil {
			panic("agent: bad tool definition " + n + ": " + err.Error())
		}
		if i > 0 {
			plain.WriteByte(',')
			cached.WriteByte(',')
		}
		plain.Write(cb.Bytes())
		if i == len(names)-1 {
			cached.Write(withCacheControl(cb.Bytes()))
		} else {
			cached.Write(cb.Bytes())
		}
	}
	plain.WriteByte(']')
	cached.WriteByte(']')
	toolsJSON, toolsJSONCached = plain.Bytes(), cached.Bytes()
}

const cacheCtl = `,"cache_control":{"type":"ephemeral"}}`

// withCacheControl returns a copy of the JSON object with a cache_control
// breakpoint appended as its last member.
func withCacheControl(obj []byte) []byte {
	out := make([]byte, 0, len(obj)+len(cacheCtl))
	out = append(out, obj[:len(obj)-1]...)
	return append(out, cacheCtl...)
}

// ---- inputs ----------------------------------------------------------------

type actInput struct {
	Steps   []Step `json:"steps"`
	Observe string `json:"observe"`
	Tab     string `json:"tab"`
	Note    string `json:"note"`
}

var refPat = regexp.MustCompile(`^e\d+$`)

func cleanRef(r string) string {
	r = strings.TrimSpace(r)
	r = strings.TrimPrefix(r, "ref=")
	r = strings.TrimPrefix(r, "@")
	return r
}

var expectKinds = map[string]bool{"text": true, "url": true, "title": true, "visible": true, "hidden": true, "count": true, "value": true}

// parseAct decodes and validates act input. Any problem is returned as an
// error whose text goes straight back to the model.
func parseAct(raw json.RawMessage, maxSteps int) (actInput, error) {
	var in actInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("invalid act input: %v", err)
	}
	if len(in.Steps) == 0 {
		return in, fmt.Errorf("act needs at least one step")
	}
	if len(in.Steps) > maxSteps {
		return in, fmt.Errorf("too many steps (%d); send at most %d per act call", len(in.Steps), maxSteps)
	}
	switch in.Observe {
	case "", "auto", "none", "full":
	default:
		return in, fmt.Errorf("observe must be auto, none or full")
	}
	for i := range in.Steps {
		if err := validateStep(&in.Steps[i]); err != nil {
			return in, fmt.Errorf("step %d (%s): %v", i+1, in.Steps[i].Op, err)
		}
	}
	return in, nil
}

func needRef(s *Step) error {
	s.Ref = cleanRef(s.Ref)
	if s.Ref == "" {
		return fmt.Errorf("ref is required")
	}
	if !refPat.MatchString(s.Ref) {
		return fmt.Errorf("ref %q is not an element ref like e41 (use refs from the snapshot, not selectors)", s.Ref)
	}
	return nil
}

func validateStep(s *Step) error {
	switch s.Op {
	case OpNavigate:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("url is required")
		}
	case OpClick, OpHover:
		return needRef(s)
	case OpType:
		return needRef(s)
	case OpSelect:
		if err := needRef(s); err != nil {
			return err
		}
		if s.Value == "" {
			return fmt.Errorf("value is required")
		}
	case OpPress:
		if strings.TrimSpace(s.Key) == "" {
			return fmt.Errorf("key is required")
		}
		if s.Ref != "" {
			return needRef(s)
		}
	case OpScroll:
		if s.Dir != "" && s.Dir != "up" && s.Dir != "down" {
			return fmt.Errorf("dir must be up or down")
		}
		if s.Amount < 0 || s.Amount > 20000 {
			return fmt.Errorf("amount out of range")
		}
	case OpWait:
		if s.Ms == 0 && s.Text == "" && s.URL == "" {
			return fmt.Errorf("wait needs ms, text or url")
		}
		if s.Ms < 0 || s.Ms > 10000 {
			return fmt.Errorf("ms must be 0..10000")
		}
	case OpBack, OpForward, OpScreenshot:
	case OpExtract:
		if s.Ref != "" {
			return needRef(s)
		}
	case OpExpect:
		if !expectKinds[s.Kind] {
			return fmt.Errorf("kind must be one of text,url,title,visible,hidden,count,value")
		}
		switch s.Kind {
		case "text", "url", "title":
			if s.Value == "" {
				return fmt.Errorf("value is required")
			}
		case "visible", "hidden":
			return needRef(s)
		default:
			if err := needRef(s); err != nil {
				return err
			}
			if s.Value == "" {
				return fmt.Errorf("value is required")
			}
		}
	case "":
		return fmt.Errorf("op is required")
	default:
		return fmt.Errorf("unknown op %q", s.Op)
	}
	return nil
}

type tabsInput struct {
	Action string `json:"action"`
	URL    string `json:"url"`
	ID     string `json:"id"`
}

func parseTabs(raw json.RawMessage) (tabsInput, error) {
	var in tabsInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("invalid tabs input: %v", err)
	}
	switch in.Action {
	case "list":
	case "open":
		if strings.TrimSpace(in.URL) == "" {
			return in, fmt.Errorf("open needs a url")
		}
	case "switch", "close":
		if in.ID == "" {
			return in, fmt.Errorf("%s needs a tab id (see tabs list)", in.Action)
		}
	default:
		return in, fmt.Errorf("action must be list, open, switch or close")
	}
	return in, nil
}

type readPageInput struct {
	Tab  string `json:"tab"`
	Mode string `json:"mode"`
}

type findInput struct {
	Query string `json:"query"`
	Role  string `json:"role"`
	Tab   string `json:"tab"`
	Limit int    `json:"limit"`
}

type askInput struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

type doneInput struct {
	Summary string `json:"summary"`
	Success *bool  `json:"success"`
}

package preview

import (
	"os"
	"testing"
	"time"
)

// TestDebug is a developer aid: DBGJS='expr' SC=scenario go test ./internal/ui/preview -run TestDebug -v
func TestDebug(t *testing.T) {
	jsExpr := os.Getenv("DBGJS")
	if jsExpr == "" {
		t.Skip("set DBGJS to use")
	}
	srv := Server()
	defer srv.Close()
	e := Engine()
	defer e.Close()
	var sc Scenario
	for _, s := range Scenarios() {
		if s.Name == os.Getenv("SC") {
			sc = s
		}
	}
	tab, _ := e.NewTab("")
	tab.AddInitScript(mockJS)
	tab.SetViewport(sc.W, sc.H, 1, false, false)
	if err := tab.Navigate(srv.URL+"/"+sc.Page+sc.Query, nil); err != nil {
		t.Fatal(err)
	}
	tab.Evaluate(`__send("state", ` + js(sc.State) + `)`)
	for i := 0; i < 4; i++ {
		tab.Settle(60 * time.Millisecond)
	}
	if sc.Setup != "" {
		tab.Evaluate(sc.Setup)
		for i := 0; i < 4; i++ {
			tab.Settle(60 * time.Millisecond)
		}
	}
	r, err := tab.Evaluate(jsExpr)
	t.Logf("result=%s err=%v", r, err)
	for _, ev := range tab.ConsoleLog(0) {
		t.Log(ev.Data)
	}
}

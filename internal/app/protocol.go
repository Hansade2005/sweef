package app

// Messages Go -> chrome page (sweef.onMessage(type, payload)):
//
//	state            full snapshot (see internal/app/state)
//	state.patch      RFC 7386 merge patch of the snapshot
//	omni.suggestions {id, items, inline}  (items carry no favicons: the dropdown draws type icons)
//	                 {id, stale:true}     the answer did not make it in time: show what you have, the real one follows
//	agent.event      {runId, kind, ...}
//	downloads.update {items:[...]} or a single item with "id"
//	toast            {text, kind}
//
// Extra messages this implementation sends (the chrome page may ignore them):
//
//	focus            {target:"chrome"|"page"}  where keyboard input now goes
//	omnibox.focus    {select:true}             Ctrl+L / new tab
//	find.open        {}                        Ctrl+F
//	context          {x,y,tab,link,image,selection,editable}  right click on a page
//	menu.open        {name, x, y}              echo of a requested menu
//	sidebar          {open,width}              sidebar toggled by shortcut
//	agent.confirm    {origin, runId}           agent wants a page whose visibility is "ask"
//
// Messages chrome page -> Go (sweef.post(type, payload)) beyond the contract:
//
//	omni.input       {text,id,scope,limit}     answered inside the same event turn (etab.omniSync): the page
//	                                           sees sweef.sync and waits for the reply instead of painting twice
//	layout           {viewport:{x,y,w,h}}      logical px; sent whenever it changes
//	ready            {}                         optional; Go also pushes state after load
//	sidebar.set      {open?, width?}
//	tab.mute         {id}
//	tab.reopen       {}
//	find.next/find.prev/find.close
//	bookmark.remove  {id}, bookmark.add {title,url,folder?}
//	downloads.pause/resume/cancel/remove {id}
//	history.clear    {since?}
//	site.privacy     {origin, noBlock}
//	zoom             {delta}

const (
	mState        = "state"
	mStatePatch   = "state.patch"
	mOmniSuggest  = "omni.suggestions"
	mAgentEvent   = "agent.event"
	mDownloadsUpd = "downloads.update"
	mToast        = "toast"
)

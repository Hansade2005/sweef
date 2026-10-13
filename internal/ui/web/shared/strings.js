/* Sweef copy deck. Every user-facing string lives here so it can be localised.
   Voice: fast, calm, confident, a little playful. Short sentences. No hype.
   {agent} and {brand} are substituted by S.f(). Rename the assistant in ONE place: S.brand. */
(function () {
  var S = window.S = {};
  S.brand = { name: "Sweef", agent: "Sidekick", agentFull: "Sweef Sidekick", tagline: "Fast. Calm. Yours." };
  S.f = function (s, v) {
    v = v || {};
    return String(s).replace(/\{(\w+)\}/g, function (m, k) { return v[k] != null ? v[k] : k === "brand" ? S.brand.name : (S.brand[k] != null ? S.brand[k] : m); });
  };
  S.tab = { new: "New tab", newTitle: "New Tab", close: "Close tab", pin: "Pin tab", unpin: "Unpin tab", mute: "Mute tab", unmute: "Unmute tab", search: "Search tabs", playing: "Playing audio", muted: "Muted", loading: "Loading", closeOthers: "Close other tabs", duplicate: "Duplicate", reload: "Reload", closeRight: "Close tabs to the right", untitled: "Untitled", crashed: "This tab crashed", list: "Tabs" };
  S.nav = { back: "Back", forward: "Forward", reload: "Reload", stop: "Stop loading", sidebar: "Toggle {agent}", menu: "Menu", downloads: "Downloads", bookmark: "Bookmark this page", unbookmark: "Remove bookmark", siteInfo: "Site information", find: "Find in page", omnibox: "Address and search bar", address: "Search or enter address", askPill: "Ask {brand}", home: "Home" };
  S.win = { min: "Minimize", max: "Maximize", restore: "Restore", close: "Close" };
  S.omni = {
    placeholder: "Search or ask {brand}", goTo: "Go to site", searchFor: "Search the web", pasteGo: "Paste and go", askFor: "Ask {brand}", askSub: "Get an answer, not ten links",
    secHistory: "History", secBookmarks: "Bookmarks", secSearch: "Search", secAsk: "Ask {brand}", secTabs: "Open tabs", switchTab: "Switch to tab",
    hint: "Enter to go", hintAsk: "Ctrl+Enter to ask", hintAskMac: "Cmd+Enter to ask", none: "Keep typing"
  };
  S.sec = { notSecure: "Not secure", warnShort: "Warning", secure: "Connection is secure", insecure: "Connection is not secure", internal: "This is a {brand} page", warning: "Certificate problem", none: "No security information", secureSub: "Your data is encrypted in transit.", insecureSub: "Anyone on the network could read or change this page. Do not enter passwords.", internalSub: "Built into {brand}. Nothing leaves your device.", warningSub: "The certificate for this site could not be verified.", certValid: "Certificate is valid", certInvalid: "Certificate is not valid", cookies: "Cookies and site data", siteSettings: "Site settings" };
  S.vis = { title: "{agent} page visibility", allowed: "Allowed", blocked: "Not allowed", ask: "Ask me first", sub: "When off, {agent} cannot see the contents of this site.", subAllowed: "{agent} can read this site when you ask it to.", subBlocked: "{agent} cannot see this site. Nothing from it is sent anywhere.", subAsk: "{agent} will ask before it reads this site.", chipOn: "{agent} can see this page", chipOff: "{agent} cannot see this page", chipAsk: "{agent} will ask first", chipToggle: "Click to change page visibility", thisPage: "this page", toastOn: "{agent} can now see {host}", toastOff: "{agent} can no longer see {host}" };
  S.menu = { newTab: "New tab", newWindow: "New window", newIncognito: "New incognito window", history: "History", downloads: "Downloads", bookmarks: "Bookmarks", settings: "Settings", zoom: "Zoom", zoomIn: "Zoom in", zoomOut: "Zoom out", find: "Find in page", print: "Print", palette: "Command palette", sidekick: "Show {agent}", hideSidekick: "Hide {agent}", help: "Help and feedback", about: "About {brand}", reportSite: "This site is broken", devtools: "Developer tools", viewSource: "View page source", savePage: "Save page as", quit: "Quit {brand}", fullscreen: "Full screen", pinned: "Pin tab", bookmarksBar: "Show bookmarks bar" };
  S.dl = { title: "Downloads", empty: "Nothing downloaded yet.", openFolder: "Show in folder", open: "Open", cancel: "Cancel", retry: "Retry", clear: "Clear all", done: "Done", failed: "Failed", paused: "Paused", all: "All downloads", of: "of", left: "left" };
  S.find = { placeholder: "Find in page", prev: "Previous match", next: "Next match", close: "Close find bar", none: "No matches", matchCase: "Match case" };
  S.compat = { text: "This site might not work yet in {brand}.", sub: "{brand} is in alpha. Some sites use features we have not built.", open: "Open in system browser", dismiss: "Dismiss", dontShow: "Do not warn for this site" };
  S.toast = { copied: "Copied", bookmarked: "Bookmarked", unbookmarked: "Bookmark removed", dlDone: "Download finished", undo: "Undo" };
  S.agent = {
    title: "{agent}", new: "New chat", close: "Close {agent}", chats: "Chats", untitled: "New chat", rename: "Rename chat", deleteChat: "Delete chat", recent: "Recent chats", noChats: "No other chats yet.",
    ask: "Ask", agent: "Agent", askTip: "Ask: answers questions about this page. It never clicks anything.", agentTip: "Agent: can click, type and navigate for you. It asks before anything sensitive.",
    placeholder: "Ask about this page", placeholderAgent: "Describe a task", followUp: "Follow up", send: "Send", stop: "Stop", attach: "Add context", voice: "Voice input is coming soon", sources: "Sources", thinking: "Thinking", thought: "Thought for {n}s", planning: "Planning", steps: "Steps", stepsDone: "{n} steps", working: "Working", youHint: "Enter to send, Shift+Enter for a new line",
    attPage: "This page", attSelection: "Selected text", attShot: "Screenshot", attTabs: "Other tabs", attFile: "Upload a file", srcWeb: "The web", attRemove: "Remove",
    retry: "Try again", approve: "Approve", deny: "Deny", confirmTitle: "Sweef wants to", confirmRisk: "Sensitive action", approved: "Approved", denied: "Denied", exactAction: "Exact action",
    errAuth: "Add your Anthropic API key to start.", errAuthBtn: "Add key", errRate: "You hit a rate limit. Try again in {s}s.", errNet: "Could not reach the model. Check your connection.", errGeneric: "Something went wrong.", errNoKey: "No API key yet",
    latency: "first token", latencyTps: "tok/s", latencyHide: "Hide speed readout", latencyTip: "Time from send to the first word. Measured on your machine.", latencyShow: "Show speed readout",
    emptyTitle: "What should we do?", emptySub: "I can read this page, answer questions about it, and act in the browser for you.", emptyAnyTitle: "Ask me anything", copy: "Copy", copied: "Copied", resize: "Resize panel", stoppedByYou: "Stopped", done: "Done", youStopped: "You stopped this run.", agentState: { idle: "Ready", working: "Working", paused: "You are in control", waiting: "Needs your OK", error: "Error", off: "No key" }, ready: "Ready"
  };
  S.starters = {
    article: [["Summarise this page", "Give me the short version in five bullets."], ["Explain it simply", "Explain this page like I am new to the topic."], ["Key facts", "List the key facts and numbers on this page."], ["Fact-check it", "Check the main claims on this page against other sources."]],
    shop: [["Compare options", "Compare the products on this page in a table."], ["Find it cheaper", "Look for a better price for this item elsewhere."], ["Add to cart", "Add the best match to my cart and stop before paying."], ["Reviews digest", "Summarise what reviewers love and hate."]],
    form: [["Fill it for me", "Fill in this form with my details. Ask me for anything you do not know."], ["What is this asking?", "Explain what each field is for."], ["Check before I send", "Review my answers for mistakes before I submit."], ["Do it for me", "Help me finish this task from start to end."]],
    video: [["Summarise this video", "Summarise the key points of this video."], ["Pull out the steps", "List any steps or instructions shown."], ["Find the good part", "Where does the main point start?"], ["Do it for me", "Help me act on what this video says."]],
    any: [["What is this page?", "Tell me what this page is and why it matters."], ["Do it for me", "Help me get something done on this page."], ["Find something", "Find the thing I am looking for on this page."], ["Open a related page", "Open the most useful related page and summarise it."]]
  };
  S.ob = {
    recommended: "Recommended",
    skip: "Skip", skipAll: "Skip setup", next: "Continue", back: "Back", finish: "Start browsing", step: "Step {n} of {total}",
    s1Title: "Welcome to {brand}", s1Sub: "A fast, light browser with a built-in {agent}. Set up takes under a minute.", importTitle: "Bring your stuff", importSub: "Bookmarks, history and saved sites. Nothing is uploaded.",
    srcChrome: "Chrome", srcEdge: "Edge", srcFirefox: "Firefox", srcSafari: "Safari", importing: "Importing", imported: "Imported", importOff: "Not available yet on this device", importTry: "Import",
    defaultTitle: "Make {brand} your default browser", defaultSub: "Links from other apps open here.", defaultBtn: "Set as default", defaultDone: "Default browser",
    themeTitle: "Pick a look", themeLight: "Light", themeDark: "Dark", themeSystem: "Match system",
    t1: "Pure Go, no Chromium", t1d: "{brand} draws pages and its own interface with its own engine.", t2: "{agent} built in", t2d: "Reads the page, answers, and acts. It asks before anything sensitive.", t3: "Your key, your data", t3d: "Your Anthropic key stays on this device. Nothing is sent until you ask.",
    s2Title: "Meet your {agent}", s2Sub: "It reads the page you are on, answers, and does the clicking for you. Watch.",
    demoUser: "Find a good beach hat under $25 and add it to my cart.", demoThink: "Looking at the page for hats, then filtering by price.", demoA1: "Searched for \"beach hat\"", demoA2: "Filtered price under $25", demoA3: "Added \"Bucket Hat\" to cart", demoDone: "Done. One hat in your cart, $19.99. I stopped before checkout.", demoReplay: "Replay",
    keyTitle: "Bring your own Anthropic key", keySub: "{agent} runs on Claude. Paste your API key to switch it on.", keyHow: "Your key stays on this device, in your system keychain (or a private file only you can read). We never see it, and it is never sent anywhere except Anthropic.", keyGet: "Get a key at console.anthropic.com", keyPlaceholder: "sk-ant-...", keySave: "Save key", keySaved: "Saved on this device", keyBad: "That does not look like an Anthropic key.", keyLater: "I will do this later",
    s3Title: "Your privacy, your call", s3Sub: "You can change all of this later in Settings.", trackTitle: "Tracker blocking", trackStd: "Standard", trackStdSub: "Blocks known trackers. Sites keep working.", trackStrict: "Strict", trackStrictSub: "Blocks more. A few sites may break, and we will tell you.", trackOff: "Off", trackOffSub: "No blocking.",
    visTitle: "Can {agent} see the pages you visit?", visAsk: "Ask me per site", visAskSub: "{agent} asks the first time on each site.", visAllowed: "Always allowed", visAllowedSub: "{agent} can read any page you ask it about.", visBlocked: "Never", visBlockedSub: "{agent} cannot see any page until you allow a site.",
    privNote: "Page contents are only sent to Anthropic when you ask {agent} something. Incognito windows are never shared.", ready: "You are set.", readySub: "Open a tab and ask for something. Your first answer should take seconds."
  };
  S.nt = {
    placeholder: "Ask or type a URL", ask: "Ask {brand}", agentMode: "Agent mode", topSites: "Top sites", statsLine: "{brand} used {mem} for {tabs} tabs today",
    r1: ["Catch me up", "Summarise the five biggest stories in tech today"], r2: ["Research for me", "Compare the best noise-cancelling headphones under $300"], r3: ["Agent mode", "Find a flight to Lisbon next month and show me the cheapest three"], r4: ["Finish a task", "Reopen my last tabs and tell me what I was reading"],
    r5: ["Learn something", "Explain how heat pumps work, with a simple diagram"], r6: ["Plan it", "Plan a three day trip to Kyoto on a modest budget"], r7: ["Agent mode", "Book a table for two on Friday at a good ramen place"], r8: ["Write with me", "Draft a polite email asking for a deadline extension"],
    incTitle: "You are in Incognito", incIntro: "What you do here is not saved in {brand}'s history and is not linked to your accounts. Incognito does not make you invisible on the internet.",
    incNotSaved: "Not saved on this device", incNotSavedBody: "{brand} will not keep your browsing history, cookies, site data, or form entries after you close all incognito windows.",
    incAgent: "{agent} in Incognito", incAgentBody: "Chats here are not saved, and nothing you do is used to build memory. Pages are only sent to Anthropic when you ask {agent} about them.",
    incNotPrivate: "Still visible to", incNotPrivateBody: "Websites you visit, sites you sign in to, your employer or school, and your internet provider can still see your activity.",
    incDownloads: "Downloads and bookmarks", incDownloadsBody: "Files you download and bookmarks you make are kept on your device after you close the window."
  };
  S.incog = { badge: "Incognito", tip: "Incognito: nothing from this window is saved to your history. It does not hide you from sites, your employer or your network." };
  S.set = {
    title: "Settings", search: "Search settings", about: "About", alpha: "Alpha", version: "Version", on: "On", off: "Off",
    nav: { general: "General", search: "Search engine", privacy: "Privacy and security", agent: "{agent}", appearance: "Appearance", downloads: "Downloads", about: "About {brand}" },
    general: { lead: "The basics.", gStart: "On startup", when: "When {brand} opens", newtab: "Open the new tab page", restore: "Continue where I left off", pages: "Open specific pages", pagesT: "Startup pages", pagesD: "Used when you pick \"Open specific pages\". One address per line.", gDef: "Default browser", isDef: "{brand} is your default browser", notDef: "{brand} is not your default browser", defD: "Links from other apps open here.", make: "Make default", defBadge: "Default" },
    search_: { lead: "What the address bar uses when you type something that is not a web address.", engine: "Search engine", engineD: "Used for searches from the address bar and new tab page.", suggest: "Show search suggestions", suggestD: "Sends what you type to your search engine so it can suggest completions.", askRow: "Show \"Ask {brand}\" in the address bar", askRowD: "A row that sends your text to {agent} instead of a search engine." },
    privacy: { lead: "{brand} blocks known trackers and tells you plainly what it does and does not do.", gTrack: "Tracking", track: "Tracker blocking", trackD: "Standard blocks known trackers. Strict also blocks more third-party content and can break some sites.", std: "Standard", strict: "Strict", off: "Off", gpc: "Send Global Privacy Control", gpcD: "Tells sites you do not want your data sold or shared.", https: "Upgrade to HTTPS when possible", httpsD: "Tries the secure version of a site first.", gData: "Your data", clearExit: "Clear browsing data on exit", clearExitD: "History, cookies and cache are removed when you quit.", clearNow: "Clear browsing data now", clearNowD: "Choose what to remove and for how long.", clearBtn: "Clear data...", site: "Site settings", siteD: "Per-site permissions and what each site stored.", open: "Open" },
    agent: { lead: "{agent} reads the page you are on, answers, and acts in the browser. You stay in control.", gModel: "Model", model: "Model", modelD: "Chosen by {brand}. It cannot be changed here.", key: "Anthropic API key", keyOn: "A key is stored on this device (OS keychain, or a private file only you can read). It is only ever sent to Anthropic.", keyOff: "No key yet. Get one at console.anthropic.com. It stays on this device.", save: "Save", remove: "Remove", gSafe: "Privacy and safety", vis: "Default page visibility", visD: "Whether {agent} can see the pages you visit. You can change this per site from the address bar.", visAsk: "Ask me per site", visAllowed: "Always allowed", visBlocked: "Never", confirm: "Ask before sensitive actions", confirmD: "{agent} stops and shows you the exact action before buying, sending or deleting anything.", mode: "Default mode", modeD: "Ask only answers. Agent can click, type and navigate.", gDisp: "Display", latency: "Show speed readout", latencyD: "Shows how long the first word took, measured on your machine.", gSites: "Sites with a saved choice", forget: "Forget", allowed: "Allowed", blocked: "Not allowed" },
    appearance: { lead: "Light, dark or match your system.", theme: "Theme", light: "Light", dark: "Dark", system: "System", density: "Density", densityD: "Compact makes the tab strip and toolbar slightly smaller.", comfy: "Comfortable", compact: "Compact", width: "{agent} width", widthD: "Drag the panel edge to resize, or pick a preset.", narrow: "Narrow", dflt: "Default", wide: "Wide", bmbar: "Show bookmarks bar" },
    downloads: { lead: "Where your files go.", loc: "Location", change: "Change...", ask: "Ask where to save each file" },
    aboutP: { lead: "A fast, light browser with an agent built in.", status: "Status: alpha", statusD: "{brand} is early. Some sites will not work yet, and we show a banner when we know that. If something breaks, open it in your system browser and tell us.", report: "Report a problem", issues: "Open issue tracker", engine: "Engine", engineD: "{brand} draws pages and its own interface with a pure Go engine. No Chromium, no Electron." },
    engines: [["duckduckgo", "DuckDuckGo"], ["google", "Google"], ["bing", "Bing"], ["brave", "Brave Search"], ["startpage", "Startpage"], ["ecosia", "Ecosia"]]
  };
  S.page = { history: "History", bookmarks: "Bookmarks", downloads: "Downloads", searchHistory: "Search history", searchBookmarks: "Search bookmarks", searchDownloads: "Search downloads", today: "Today", yesterday: "Yesterday", clear: "Clear browsing data", empty: "Nothing here yet.", emptyHist: "Pages you visit will show up here.", emptyBm: "Star a page to find it here.", remove: "Remove", noResults: "No results for \"{q}\"", openAll: "Open in new tab", loadMore: "Show more", items: "{n} items" };
  S.err = {
    offline: { title: "You are offline", body: "{brand} cannot reach the internet. Check your Wi-Fi or cable, then try again.", code: "NET_OFFLINE" },
    dns: { title: "Cannot find this site", body: "The address {host} did not resolve. It may be misspelled, or the site may be down.", code: "DNS_NOT_FOUND" },
    cert: { title: "This connection is not private", body: "The certificate for {host} is not valid. Someone could be pretending to be this site, or the site may be set up wrong. We do not recommend continuing.", code: "CERT_INVALID", details: "Reason" },
    crash: { title: "This tab crashed", body: "Something went wrong while drawing this page. Your other tabs are fine.", code: "TAB_CRASHED" },
    timeout: { title: "This site took too long", body: "{host} did not answer in time. It may be busy.", code: "TIMEOUT" },
    tips: { offline: ["Check that Wi-Fi or Ethernet is connected.", "Restart your router if other devices are offline too.", "A VPN or proxy can also block the connection."], dns: ["Check the address for typos.", "Try again in a minute. The site may be down.", "If other sites load, the problem is with this one."], timeout: ["The site may be busy. Try again shortly.", "Check that your connection is stable."], crash: ["Reloading usually fixes it.", "If it keeps happening, tell us what site it was."], cert: [] },
    reload: "Try again", external: "Open in system browser", fix: "Ask {brand} to fix this", proceed: "Continue anyway (unsafe)", details: "Technical details", back: "Go back", fixPrompt: "This page failed to load ({code}) for {url}. Work out what is wrong and help me fix it."
  };
  S.cmd = { placeholder: "Search tabs, history, commands, or ask {brand}", tabs: "Tabs", commands: "Commands", history: "History", bookmarks: "Bookmarks", ask: "Ask {brand}: {q}", none: "Nothing found. Press Enter to ask {brand}.", hint: "Enter to run" };
  S.cmds = [
    ["New tab", "tab.new", "Ctrl+T"], ["New incognito window", "incognito.new", "Ctrl+Shift+N"], ["Close tab", "tab.closeActive", "Ctrl+W"], ["Reload page", "nav.reload", "Ctrl+R"],
    ["Toggle {agent}", "sidebar.toggle", "Ctrl+Shift+A"], ["New {agent} chat", "agent.chat.new", ""], ["Find in page", "find.open", "Ctrl+F"], ["Bookmark this page", "bookmark.toggle", "Ctrl+D"],
    ["Open settings", "page:sweef://settings", ""], ["Open history", "page:sweef://history", "Ctrl+H"], ["Open downloads", "page:sweef://downloads", "Ctrl+J"], ["Open bookmarks", "page:sweef://bookmarks", ""],
    ["Switch theme: light", "theme:light", ""], ["Switch theme: dark", "theme:dark", ""], ["Switch theme: match system", "theme:system", ""]
  ];
  S.overlay = { working: "{agent} is working", fulfilling: "Fulfilling \"{task}\"", takeControl: "Take control", stop: "Stop", control: "You are in control", controlSub: "{agent} is paused.", resume: "Hand back to {agent}", step: "Step {n}", done: "Done", stopped: "Stopped", cursorLabel: "{agent}" };

  /* Data Saver. Plain words, never guilt: say what is happening and what the person can do.
     "Measured" = counted from the network. "Estimated" = our best guess. The UI never adds the two. */
  S.data = {
    name: "Data Saver", chipTip: "{used} of {limit} used {period}", chipTipFree: "{used} used {period}", chipAria: "Data used {period}: {used} of {limit}", chipAriaFree: "Data used {period}: {used}",
    period: { day: "today", week: "this week", month: "this month" }, periodName: { day: "day", week: "week", month: "month" },
    lvl: { off: "Off", standard: "Standard", strict: "Text first" },
    left: "left", of: "of", usedWord: "used", resetsIn: "Resets in {n} days", resetsTomorrow: "Resets tomorrow", resetsOn: "Resets {date}", noLimit: "No limit set", setLimit: "Set a limit",
    thisPage: "This page", usedHere: "Used on this page", approx: "(approx.)", usedHereTip: "Counted from each response as it arrived. It can differ slightly from the total on the wire.",
    savedM: "Saved by cache and limits", savedMTip: "Files we did not download again, or stopped downloading because they were over the size limit. Counted exactly.",
    notLoaded: "Not loaded", notLoadedReq: "{n} requests not loaded", notLoadedReq1: "1 request not loaded", about: "about", estTip: "An estimate from typical sizes of the files we skipped. It may be higher or lower than the real size.",
    measured: "Measured", estimate: "Estimate", legend: "Estimates are never added to measured numbers.",
    nothingHere: "Nothing counted on this page yet.", noMeter: "Counting is not available in this build yet, so there are no numbers to show.",
    loadImages: "Load images on this page", loadImagesN: "Load images on this page ({n})", loadFully: "Load fully, once", loadFullyDone: "Loaded fully", loadFullyTip: "Reloads this page with everything on. Next page goes back to your settings.",
    siteRule: "For {host}", ruleAuto: "Auto", ruleFull: "Always full", ruleLite: "Always lite", ruleSub: { "": "Follows your level.", full: "Everything loads on this site.", lite: "This site always loads in a lighter way." },
    top: "Top sites {period}", topNote: "Approximate", topNone: "Sites will show up here as you browse.", settings: "Data settings", mode: "Level",
    tapHint: "Or tap a box on the page to load just that one.", incogNote: "Private windows count toward your budget. {brand} does not keep which sites they used.",
    research: "Research used {n}", researchNot: "About {n} not loaded (estimate)", researchTip: "Counted from the pages {agent} opened. They load as text, without images or video.",
    nt: { left: "{n} left {period}", used: "You have used your {limit} {period}. It resets {when}.", tomorrow: "tomorrow", inDays: "in {n} days" },
    stop: {
      title: "You have used your {limit} {period}", sub: "{brand} paused new pages so your data plan stays safe. Your budget resets {when}.", once: "Load this page once", onceNote: "Loading once still counts toward your total.",
      raise: "Raise limit", wait: "Wait until reset", raiseTitle: "New limit", raiseGo: "Raise and load", noLimit: "No limit", back: "Back", host: "You asked for"
    },
    set: {
      nav: "Data", lead: "Say how much data you have and {brand} helps you stretch it. Everything happens on this device: pages are never sent through a server to shrink them.",
      gLevel: "Data Saver level", levelOff: "Off", levelOffTag: "Count only", levelStd: "Standard", levelStdTag: "Lighter pages, same look", levelStrict: "Text first", levelStrictTag: "Pages as text, pictures on tap",
      does: "What it does", doesnt: "What it does not do", recommended: "Good for most plans",
      offDo: ["Counts the data you use", "Keeps your budget and tells you as you get close"], offDont: ["Does not block or delay anything", "Does not change how pages look"],
      stdDo: ["Loads images only as you scroll near them", "Turns very large images (over 200 KB) into tap-to-load boxes", "Plays video and audio only when you press play", "Pauses background tabs and reuses saved files instead of downloading again"],
      stdDont: ["Does not change text, layout or buttons", "Does not send pages through another company's servers", "Anything you tap loads in full"],
      strictDo: ["Everything in Standard", "Shows no images until you tap them; boxes keep the page layout", "Skips video, audio and scripts from other sites", "Limits the site's own scripts to 300 KB per page"],
      strictDont: ["Keeps page colours and layout (styles stay on)", "Has a Load fully, once button on every page", "Some interactive sites may look plain until you load them fully"],
      gBudget: "Data budget", budgetD: "Pick your plan size. {brand} only counts what pages load, and never stops you unless you ask it to.", presets: [["100", "100 MB"], ["500", "500 MB"], ["1024", "1 GB"], ["0", "No limit"]],
      custom: "Custom size", customD: "In megabytes.", period: "Budget period", periodD: "How often your data allowance starts over.", day: "Day", week: "Week", month: "Month",
      resetDay: "Resets on", resetDayWeek: "Week starts on", resetDayMonth: "Day of the month", weekdays: ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"],
      warn: "Heads-up", warnD: "{brand} tells you once at {a}% and {b}% of your budget. No nagging.", hardStop: "Stop loading pages when the budget is used up",
      hardStopD: "You can still load one page at a time, raise the limit, or wait for the reset. Off by default: pages keep loading and you just see the numbers.",
      gUse: "So far {period}", usedL: "Used", usedLD: "Counted from the network as pages arrive.", savedL: "Saved, measured", savedLD: "Cache hits and downloads stopped early. Counted exactly.", estL: "Not loaded, estimate", estLD: "A guess from typical sizes. Never added to the measured numbers.",
      top: "Busiest sites", topD: "Approximate, by site.", reset: "Reset counter", resetD: "Start counting from zero. Your budget stays as it is.", resetSure: "Reset the usage counter?", resetYes: "Yes, reset", cancel: "Cancel",
      gMore: "More", agentLite: "Light loading for {agent}", agentLiteD: "When {agent} researches for you, pages load as text without images or video. It never needs them.", showMeter: "Show the data meter in the toolbar", showMeterD: "A small chip next to the address bar.",
      gSites: "Sites with their own rule", sitesD: "Set from the data popover on any page.", sitesNone: "No site has its own rule yet.", forget: "Forget", ruleFull: "Always full", ruleLite: "Always lite",
      gStorage: "Site storage", storageD: "Data that sites keep on this device: databases, caches and files. {used} of {budget} in use. When space runs short, the sites you used least recently are cleared first, except the ones marked kept.", storageNone: "No site is storing data on this device.", clearSite: "Clear", kept: "kept",
      gCount: "What is counted", countD: "Used is counted from the network, including encryption overhead. Downloads and favicons are included; {agent}'s own messages to the model are not. Saved is split in two: measured (files we did not download) and estimated (a guess from typical sizes). We never add them together.",
      unavailable: "Counting is not available in this build yet, so the numbers below stay empty."
    },
    ob: {
      title: "Do you have a data plan?", sub: "If your data is limited, say so and {brand} will help it last. Change it any time in Settings.",
      opts: [["100", "100 MB", "a month", "Text first", "Pages load as text. Pictures when you tap."], ["500", "500 MB", "a month", "Standard", "Lighter pages that still look like pages."], ["1024", "1 GB", "a month", "Standard", "Lighter pages that still look like pages."], ["0", "No limit", "unlimited", "Off", "Nothing held back. You can still see what you use."]],
      notSure: "I am not sure", picked: "Starting with {lvl}. You can switch any time.", skipped: "No problem. You can set this later in Settings."
    }
  };
  S.stat = { mem: "Memory", tabs: "Tabs", blocked: "Blocked today" };
})();

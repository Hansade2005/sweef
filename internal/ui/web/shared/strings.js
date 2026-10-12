/* Sweef copy deck. Every user-facing string lives here so it can be localised.
   Voice: fast, calm, confident, a little playful. Short sentences. No hype.
   {agent} and {brand} are substituted by S.f(). Rename the assistant in ONE place: S.brand. */
(function () {
  var S = window.S = {};
  S.brand = { name: "Sweef", agent: "Sidekick", agentFull: "Sweef Sidekick", tagline: "Fast. Calm. Yours." };
  S.f = function (s, v) {
    v = v || {};
    return String(s).replace(/\{(\w+)\}/g, function (m, k) { return v[k] != null ? v[k] : (S.brand[k] != null ? S.brand[k] : m); });
  };
  S.tab = { new: "New tab", newTitle: "New Tab", close: "Close tab", pin: "Pin tab", unpin: "Unpin tab", mute: "Mute tab", unmute: "Unmute tab", search: "Search tabs", playing: "Playing audio", muted: "Muted", loading: "Loading", closeOthers: "Close other tabs", duplicate: "Duplicate", reload: "Reload", closeRight: "Close tabs to the right", untitled: "Untitled", crashed: "This tab crashed", list: "Tabs" };
  S.nav = { back: "Back", forward: "Forward", reload: "Reload", stop: "Stop loading", sidebar: "Toggle {agent}", menu: "Menu", downloads: "Downloads", bookmark: "Bookmark this page", unbookmark: "Remove bookmark", siteInfo: "Site information", find: "Find in page", omnibox: "Address and search bar", address: "Search or enter address", askPill: "Ask {brand}", home: "Home" };
  S.win = { min: "Minimize", max: "Maximize", restore: "Restore", close: "Close" };
  S.omni = {
    placeholder: "Search or ask {brand}", goTo: "Go to", searchFor: "Search for", pasteGo: "Paste and go", askFor: "Ask {brand}", askSub: "Get an answer, not ten links",
    secHistory: "History", secBookmarks: "Bookmarks", secSearch: "Search", secAsk: "Ask {brand}", secTabs: "Open tabs", switchTab: "Switch to tab",
    hint: "Enter to go", hintAsk: "Ctrl+Enter to ask", hintAskMac: "Cmd+Enter to ask", none: "Keep typing"
  };
  S.sec = { secure: "Connection is secure", insecure: "Connection is not secure", internal: "This is a {brand} page", warning: "Certificate problem", none: "No security information", secureSub: "Your data is encrypted in transit.", insecureSub: "Anyone on the network could read or change this page. Do not enter passwords.", internalSub: "Built into {brand}. Nothing leaves your device.", warningSub: "The certificate for this site could not be verified.", certValid: "Certificate is valid", certInvalid: "Certificate is not valid", cookies: "Cookies and site data", siteSettings: "Site settings" };
  S.vis = { title: "{agent} page visibility", allowed: "Allowed", blocked: "Not allowed", ask: "Ask me first", sub: "When off, {agent} cannot see the contents of this site.", subAllowed: "{agent} can read this site when you ask it to.", subBlocked: "{agent} cannot see this site. Nothing from it is sent anywhere.", subAsk: "{agent} will ask before it reads this site.", chipOn: "{agent} can see this page", chipOff: "{agent} cannot see this page", chipAsk: "{agent} will ask first", chipToggle: "Click to change page visibility", thisPage: "this page", toastOn: "{agent} can now see {host}", toastOff: "{agent} can no longer see {host}" };
  S.menu = { newTab: "New tab", newWindow: "New window", newIncognito: "New incognito window", history: "History", downloads: "Downloads", bookmarks: "Bookmarks", settings: "Settings", zoom: "Zoom", find: "Find in page", print: "Print", palette: "Command palette", sidekick: "Show {agent}", hideSidekick: "Hide {agent}", help: "Help and feedback", about: "About {brand}", reportSite: "This site is broken", devtools: "Developer tools", viewSource: "View page source", savePage: "Save page as", quit: "Quit {brand}", fullscreen: "Full screen", pinned: "Pin tab", bookmarksBar: "Show bookmarks bar" };
  S.dl = { title: "Downloads", empty: "Nothing downloaded yet.", openFolder: "Show in folder", open: "Open", cancel: "Cancel", retry: "Retry", clear: "Clear all", done: "Done", failed: "Failed", paused: "Paused", all: "All downloads", of: "of", left: "left" };
  S.find = { placeholder: "Find in page", prev: "Previous match", next: "Next match", close: "Close find bar", none: "No matches", matchCase: "Match case" };
  S.compat = { text: "This site might not work yet in {brand}.", sub: "{brand} is in alpha. Some sites use features we have not built.", open: "Open in system browser", dismiss: "Dismiss", dontShow: "Do not warn for this site" };
  S.toast = { copied: "Copied", bookmarked: "Bookmarked", unbookmarked: "Bookmark removed", dlDone: "Download finished", undo: "Undo" };
  S.agent = {
    title: "{agent}", new: "New chat", close: "Close {agent}", chats: "Chats", untitled: "New chat", rename: "Rename chat", deleteChat: "Delete chat", recent: "Recent chats", noChats: "No other chats yet.",
    ask: "Ask", agent: "Agent", askTip: "Ask: answers questions about this page. It never clicks anything.", agentTip: "Agent: can click, type and navigate for you. It asks before anything sensitive.",
    placeholder: "Ask about this page", placeholderAgent: "Describe a task", followUp: "Follow up", send: "Send", stop: "Stop", attach: "Add context", voice: "Voice input is coming soon", sources: "Sources", thinking: "Thinking", thought: "Thought for {n}s", planning: "Planning", steps: "Steps", stepsDone: "{n} steps", working: "Working", youHint: "Enter to send, Shift+Enter for a new line",
    attPage: "This page", attSelection: "Selected text", attShot: "Screenshot", attTabs: "Other tabs", attFile: "Upload a file", attRemove: "Remove",
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
    skip: "Skip", skipAll: "Skip setup", next: "Continue", back: "Back", finish: "Start browsing", step: "Step {n} of {total}",
    s1Title: "Welcome to {brand}", s1Sub: "A fast, light browser with a built-in {agent}. Set up takes under a minute.", importTitle: "Bring your stuff", importSub: "Bookmarks, history and saved sites. Nothing is uploaded.",
    srcChrome: "Chrome", srcEdge: "Edge", srcFirefox: "Firefox", srcSafari: "Safari", importing: "Importing", imported: "Imported", importOff: "Not available yet on this device", importTry: "Import",
    defaultTitle: "Make {brand} your default browser", defaultSub: "Links from other apps open here.", defaultBtn: "Set as default", defaultDone: "Default browser",
    themeTitle: "Pick a look", themeLight: "Light", themeDark: "Dark", themeSystem: "Match system",
    s2Title: "Meet your {agent}", s2Sub: "It reads the page you are on, answers, and does the clicking for you. Watch.",
    demoUser: "Find a good beach hat under $25 and add it to my cart.", demoThink: "Looking at the page for hats, then filtering by price.", demoA1: "Searched for \"beach hat\"", demoA2: "Filtered price under $25", demoA3: "Added \"Bucket Hat\" to cart", demoDone: "Done. One hat in your cart, $19.99. I stopped before checkout.", demoReplay: "Replay",
    keyTitle: "Bring your own Anthropic key", keySub: "{agent} runs on Claude. Paste your API key to switch it on.", keyHow: "Your key stays on this device, in your system keychain (or a private file only you can read). We never see it, and it is never sent anywhere except Anthropic.", keyGet: "Get a key at console.anthropic.com", keyPlaceholder: "sk-ant-...", keySave: "Save key", keySaved: "Saved on this device", keyBad: "That does not look like an Anthropic key.", keyLater: "I will do this later",
    s3Title: "Your privacy, your call", s3Sub: "You can change all of this later in Settings.", trackTitle: "Tracker blocking", trackStd: "Standard", trackStdSub: "Blocks known trackers. Sites keep working.", trackStrict: "Strict", trackStrictSub: "Blocks more. A few sites may break, and we will tell you.", trackOff: "Off", trackOffSub: "No blocking.",
    visTitle: "Can {agent} see the pages you visit?", visAsk: "Ask me per site", visAskSub: "Recommended. {agent} asks the first time on each site.", visAllowed: "Always allowed", visAllowedSub: "{agent} can read any page you ask it about.", visBlocked: "Never", visBlockedSub: "{agent} cannot see any page until you allow a site.",
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
  S.set = {
    title: "Settings", search: "Search settings", about: "About", alpha: "Alpha", version: "Version",
    nav: { general: "General", search: "Search engine", privacy: "Privacy and security", agent: "{agent}", appearance: "Appearance", downloads: "Downloads", startup: "On startup", default: "Default browser", about: "About {brand}" }
  };
  S.page = { history: "History", bookmarks: "Bookmarks", downloads: "Downloads", searchHistory: "Search history", searchBookmarks: "Search bookmarks", searchDownloads: "Search downloads", today: "Today", yesterday: "Yesterday", clear: "Clear browsing data", empty: "Nothing here yet.", emptyHist: "Pages you visit will show up here.", emptyBm: "Star a page to find it here.", remove: "Remove", noResults: "No results for \"{q}\"", openAll: "Open in new tab", loadMore: "Show more", items: "{n} items" };
  S.err = {
    offline: { title: "You are offline", body: "{brand} cannot reach the internet. Check your Wi-Fi or cable, then try again.", code: "NET_OFFLINE" },
    dns: { title: "Cannot find this site", body: "The address {host} did not resolve. It may be misspelled, or the site may be down.", code: "DNS_NOT_FOUND" },
    cert: { title: "This connection is not private", body: "The certificate for {host} is not valid. Someone could be pretending to be this site, or the site may be set up wrong. We do not recommend continuing.", code: "CERT_INVALID", details: "Reason" },
    crash: { title: "This tab crashed", body: "Something went wrong while drawing this page. Your other tabs are fine.", code: "TAB_CRASHED" },
    timeout: { title: "This site took too long", body: "{host} did not answer in time. It may be busy.", code: "TIMEOUT" },
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
  S.stat = { mem: "Memory", tabs: "Tabs", blocked: "Blocked today" };
})();

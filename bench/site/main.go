// Command site serves the benchmark fixture pages: a landing page with
// navigation, a login form, and a React counter app (bundles vendored under
// internal/engine/testdata/vendor).
package main

import (
	"flag"
	"fmt"
	"net/http"
)

const landing = `<!doctype html><html><head><title>Acme</title></head><body>
<header><nav><a href="/">Home</a> <a href="/pricing">Pricing</a> <a href="/docs">Docs</a> <a href="/login">Sign in</a></nav></header>
<main>
<h1>Ship faster with Acme</h1>
<p>Acme is the platform teams use to build, test and deploy. Join 12,000 companies that trust Acme to run their agents in production.</p>
<ul><li>Zero-config deploys</li><li>Built-in observability</li><li>Pay only for what you use</li></ul>
<h2>Plans</h2>
<table><tr><th>Plan</th><th>Price</th></tr><tr><td>Free</td><td>$0</td></tr><tr><td>Pro</td><td>$20</td></tr><tr><td>Team</td><td>$40 per seat</td></tr></table>
<button id="cta">Start free</button>
</main>
<footer><a href="/terms">Terms</a> <a href="/privacy">Privacy</a></footer>
</body></html>`

const login = `<!doctype html><html><head><title>Sign in</title></head><body>
<h1>Sign in to Acme</h1>
<form action="/welcome" method="get">
<label for="email">Email</label><input id="email" name="email" type="email" required>
<label for="password">Password</label><input id="password" name="password" type="password">
<label><input type="checkbox" name="remember"> Remember me</label>
<button type="submit">Sign in</button>
</form>
<p>New here? <a href="/signup">Create an account</a></p>
</body></html>`

const welcome = `<!doctype html><html><head><title>Welcome</title></head><body><h1>Welcome back</h1><p>You are signed in.</p></body></html>`

const app = `<!doctype html><html><head><title>Counter</title></head><body><div id="root"></div>
<script src="/vendor/react.js"></script><script src="/vendor/react-dom.js"></script>
<script>
const e = React.createElement;
function App(){ const [n,setN]=React.useState(0); return e('div',null,e('h1',null,'Count: '+n),e('button',{id:'inc',onClick:()=>setN(n+1)},'Increment'),e('ul',null,Array.from({length:n},(_,i)=>e('li',{key:i},'row '+i)))); }
ReactDOM.createRoot(document.getElementById('root')).render(e(App));
</script></body></html>`

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()
	page := func(p, body string) {
		http.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		})
	}
	page("/", landing)
	page("/login", login)
	page("/welcome", welcome)
	page("/app", app)
	for _, p := range []string{"/pricing", "/docs", "/terms", "/privacy", "/signup"} {
		page(p, "<html><head><title>"+p+"</title></head><body><h1>"+p+"</h1><a href='/'>Home</a></body></html>")
	}
	http.Handle("/vendor/", http.StripPrefix("/vendor/", http.FileServer(http.Dir("internal/engine/testdata/vendor"))))
	fmt.Println("fixture site on", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		panic(err)
	}
}

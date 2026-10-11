# sweef-browser (Python SDK)

Synchronous, Playwright-shaped, stdlib only.

```python
from sweef import Sweef

with Sweef.launch() as sw:                       # starts `sweef serve`; or Sweef.connect("127.0.0.1:9222")
    page = sw.new_page("https://example.com")
    print(page.title())
    print(page.aria_snapshot(interactive_only=True))
    page.click("text=Learn more")
    print(page.evaluate("document.querySelectorAll('p').length"))
    page.route("**/*.png", status=404, action="abort")
```

Install: `pip install -e pkg/sdk/py`. Set `SWEEF_BIN` if the binary is not on `PATH`.

# sweef-browser (Python SDK)

```python
from sweef import Sweef

with Sweef.launch("127.0.0.1:9222") as sw:      # or Sweef.connect(addr) to an existing `sweef serve`
    page = sw.new_page("https://example.com")
    print(page.title())
    page.click("text=More information")
    print(page.aria_snapshot())
    page.evaluate("document.title.length")
    page.route("**/*.png", status=404, action="abort")   # mock network
```

Install: `pip install -e .` (no third-party deps — stdlib only).

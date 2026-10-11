# @sweef/browser (Node.js SDK)

```js
const { Sweef } = require('@sweef/browser');

const sw = await Sweef.connect();                    // to a running `sweef serve`
const page = await sw.newPage('https://example.com');
console.log(await page.title());
await page.click('text=More information');
console.log(await page.ariaSnapshot());
await page.evaluate('document.title.length');
await page.route('**/*.png', { status: 404, action: 'abort' });
sw.close();
```

Zero dependencies (`net` only). Types included.

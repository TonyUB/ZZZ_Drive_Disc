const fs = require('fs');

const html = fs.readFileSync('web/index.html', 'utf8')
  .replaceAll('__APP_RELEASE__', 'v1.0B')
  .replaceAll('__SCANNER_AVAILABLE__', 'true')
  .replaceAll('<!--__SCANNER_BUTTON__-->', '<button id="startScannerBtn"></button>');
const scriptPattern = /<script(?:\s[^>]*)?>([\s\S]*?)<\/script>/g;
let checked = 0;

for (const match of html.matchAll(scriptPattern)) {
  const source = match[1].trim();
  if (!source) continue;
  new Function(source);
  checked += 1;
}

if (checked === 0) {
  throw new Error('No inline JavaScript blocks found.');
}

console.log(`INLINE_JS_OK blocks=${checked}`);

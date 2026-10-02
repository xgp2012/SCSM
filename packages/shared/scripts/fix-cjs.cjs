// 为 CJS 产物写一个 package.json，声明 type=commonjs，
// 否则会被包根的 "type": "module" 误判为 ESM。
const fs = require('node:fs');
const path = require('node:path');
const dir = path.join(__dirname, '..', 'dist', 'cjs');
fs.mkdirSync(dir, { recursive: true });
fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify({ type: 'commonjs' }, null, 2));

// Windows PowerShell 5.1 decodes a script without a BOM as ANSI, which breaks
// non-ASCII comments and string literals. The spike scripts are therefore kept
// as UTF-8 with BOM; run this after editing them.
const fs = require('fs');
const path = require('path');
const dir = __dirname;
const BOM = '\ufeff';
for (const name of fs.readdirSync(dir)) {
  if (!name.endsWith('.ps1')) continue;
  const file = path.join(dir, name);
  const text = fs.readFileSync(file, 'utf8').replace(/^\ufeff/, '');
  fs.writeFileSync(file, BOM + text, 'utf8');
  console.log('bom: ' + name);
}

#!/usr/bin/env node
const fs = require('fs');
const path = require('path');
const root = path.resolve(__dirname, '..');
const js = fs.readFileSync(path.join(__dirname, 'interface.js'), 'utf8');
if (/progressStatus[\s\S]{0,500}(complete|completed)/i.test(js)) throw new Error('interface adapter infers evidence from status prose');
for (const page of ['index.html','alternate.html','phosphor.html']) {
  const html = fs.readFileSync(path.join(root, page), 'utf8');
  const count = (html.match(/js\/interface\.js/g) || []).length;
  if (count !== 1) throw new Error(`${page}: expected one interface.js, got ${count}`);
}
for (const outcome of ['pending','running','succeeded','unavailable','failed']) {
  if (!js.includes(`'${outcome}'`)) throw new Error(`missing structured outcome ${outcome}`);
}
if (!js.includes("params.get('r')")) throw new Error('shared-result preservation missing');
console.log('interface contract ok');

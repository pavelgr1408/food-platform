// Валидация AsyncAPI-файлов: node validate.mjs <файл>...
// Ошибки парсера/правил (severity error) → код возврата 1; предупреждения и подсказки печатаются, но не блокируют.
import { readFileSync } from 'node:fs';
import { Parser } from '@asyncapi/parser';

const files = process.argv.slice(2);
if (files.length === 0) {
  console.error('usage: node validate.mjs <asyncapi.yaml>...');
  process.exit(2);
}
const names = ['error', 'warning', 'information', 'hint'];
const parser = new Parser();
let failed = 0;
for (const f of files) {
  const { document, diagnostics } = await parser.parse(readFileSync(f, 'utf8'), { source: f });
  const errors = diagnostics.filter((d) => d.severity === 0);
  for (const d of diagnostics) {
    const line = d.range ? d.range.start.line + 1 : '?';
    console.log(`${f}:${line} ${names[d.severity] ?? d.severity} ${d.code}: ${d.message}`);
  }
  if (errors.length > 0 || !document) {
    failed++;
    console.error(`FAIL ${f}: ${errors.length} error(s)`);
  } else {
    console.log(`OK   ${f}: AsyncAPI ${document.version()} valid, ${diagnostics.length} notice(s)`);
  }
}
process.exit(failed ? 1 : 0);

// Генерация TS-типов из contracts/openapi/*.yaml (contract-first, ADR-5).
// node scripts/gen.mjs          — перегенерировать src/generated/*.ts
// node scripts/gen.mjs --check  — упасть, если сгенерированное расходится с контрактом (для CI)
import { readdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { dirname, join } from "node:path";
import openapiTS, { astToString } from "openapi-typescript";

const here = dirname(fileURLToPath(import.meta.url));
const specDir = join(here, "../../../../contracts/openapi");
const outDir = join(here, "../src/generated");
const check = process.argv.includes("--check");
const header = "/* Сгенерировано scripts/gen.mjs из contracts/openapi. НЕ РЕДАКТИРОВАТЬ. */\n";

let drift = 0;
for (const f of readdirSync(specDir).filter((n) => n.endsWith(".yaml") && !n.startsWith("_"))) {
  const name = f.replace(".yaml", "");
  const ast = await openapiTS(pathToFileURL(join(specDir, f)), { alphabetize: true });
  const text = header + astToString(ast);
  const out = join(outDir, `${name}.ts`);
  if (check) {
    if (!existsSync(out) || readFileSync(out, "utf8") !== text) { console.error(`drift: ${name}`); drift++; }
  } else {
    writeFileSync(out, text);
    console.log(`ok ${name}`);
  }
}
if (drift) process.exit(1);

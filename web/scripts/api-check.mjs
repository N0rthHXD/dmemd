import { execFileSync } from 'node:child_process';
import { readFileSync, rmSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const output = resolve(root, '.schema.check.ts');
const binary = resolve(root, 'node_modules', '.bin', 'openapi-typescript');
const committed = resolve(root, 'src', 'shared', 'api', 'schema.gen.ts');

try {
  execFileSync(binary, [resolve(root, '..', 'api', 'openapi.yaml'), '-o', output], {
    stdio: 'inherit',
  });
  const expected = readFileSync(committed, 'utf8');
  const actual = readFileSync(output, 'utf8');
  if (expected !== actual) {
    console.error('schema.gen.ts 与 api/openapi.yaml 生成结果不一致，请运行 npm run api:generate。');
    process.exitCode = 1;
  }
} finally {
  rmSync(output, { force: true });
}

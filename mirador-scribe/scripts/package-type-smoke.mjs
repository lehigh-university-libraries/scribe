import { execFileSync } from 'node:child_process';
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const temporaryRoot = mkdtempSync(join(tmpdir(), 'mirador-scribe-package-types-'));

try {
  const packedFilename = execFileSync(
    'npm',
    ['pack', '--silent', '--pack-destination', temporaryRoot],
    { cwd: packageRoot, encoding: 'utf8' },
  ).trim().split(/\r?\n/).at(-1);
  if (!packedFilename) throw new Error('npm pack did not return an archive name');

  const consumerRoot = join(temporaryRoot, 'consumer');
  const installedRoot = join(consumerRoot, 'node_modules', 'mirador-scribe');
  mkdirSync(installedRoot, { recursive: true });
  execFileSync('tar', [
    '-xzf',
    join(temporaryRoot, packedFilename),
    '--strip-components=1',
    '-C',
    installedRoot,
  ]);

  const installedManifest = JSON.parse(readFileSync(join(installedRoot, 'package.json'), 'utf8'));
  if (installedManifest.types !== 'dist/index.d.ts') {
    throw new Error('packed package does not resolve declarations from dist/index.d.ts');
  }
  for (const relativePath of ['dist/index.d.ts', 'dist/types/scribe.d.ts']) {
    if (!existsSync(join(installedRoot, relativePath))) {
      throw new Error(`packed package is missing ${relativePath}`);
    }
  }

  copyFileSync(join(packageRoot, 'test-dts', 'consumer.ts'), join(consumerRoot, 'consumer.ts'));
  writeFileSync(join(consumerRoot, 'tsconfig.json'), `${JSON.stringify({
    compilerOptions: {
      lib: ['ES2022', 'DOM'],
      module: 'ESNext',
      moduleResolution: 'Bundler',
      noEmit: true,
      skipLibCheck: false,
      strict: true,
      target: 'ES2022',
    },
    include: ['consumer.ts'],
  }, null, 2)}\n`);

  execFileSync(process.execPath, [
    join(packageRoot, 'node_modules', 'typescript', 'bin', 'tsc'),
    '--project',
    join(consumerRoot, 'tsconfig.json'),
  ], { cwd: consumerRoot, stdio: 'inherit' });
} finally {
  rmSync(temporaryRoot, { force: true, recursive: true });
}

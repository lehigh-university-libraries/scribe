import react from '@vitejs/plugin-react';
import { copyFileSync, mkdirSync } from 'node:fs';
import pkg from './package.json';

const peers = Object.keys(pkg.peerDependencies || {});

export default {
  build: {
    lib: {
      entry: './src/index.js',
      fileName: (format) => format === 'es' ? 'mirador-scribe.mjs' : 'mirador-scribe.cjs',
      formats: ['es', 'cjs'],
      name: 'MiradorScribePlugin',
    },
    rollupOptions: {
      external: [
        ...peers,
        /^react(\/.*)?$/,
        /^react-dom(\/.*)?$/,
        /^@mui\/material(\/.*)?$/,
        /^@mui\/system(\/.*)?$/,
        /^@emotion\/react(\/.*)?$/,
        /^@emotion\/styled(\/.*)?$/,
        /^mirador(\/.*)?$/,
        /^openseadragon(\/.*)?$/,
        'i18next',
        'react-i18next',
      ],
      output: {
        exports: 'named',
      },
    },
    sourcemap: true,
  },
  plugins: [
    react(),
    {
      closeBundle() {
        mkdirSync('dist/types', { recursive: true });
        copyFileSync('src/index.d.ts', 'dist/index.d.ts');
        copyFileSync('src/types/scribe.d.ts', 'dist/types/scribe.d.ts');
      },
      name: 'copy-types',
    },
  ],
  resolve: {
    dedupe: [
      'react',
      'react-dom',
      '@mui/material',
      '@mui/system',
      '@emotion/react',
      '@emotion/styled',
    ],
  },
};

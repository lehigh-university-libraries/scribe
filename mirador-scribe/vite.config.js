import react from '@vitejs/plugin-react';
import pkg from './package.json';

const peers = Object.keys(pkg.peerDependencies || {});

export default {
  build: {
    lib: {
      entry: './src/index.js',
      fileName: (format) => `mirador-scribe.${format}.js`,
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
        'i18next',
        'react-i18next',
      ],
      output: {
        exports: 'named',
      },
    },
    sourcemap: true,
  },
  esbuild: { include: [/src\/.*\.jsx?$/], loader: 'jsx' },
  plugins: [react()],
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

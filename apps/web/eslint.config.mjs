import withNuxt from './.nuxt/eslint.config.mjs';

export default withNuxt({
  ignores: ['dist/**', '.output/**', '.nuxt/**', 'node_modules/**', 'e2e-report/**', 'playwright-report/**'],
});

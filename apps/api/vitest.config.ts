import { defineConfig } from 'vitest/config';
import swc from 'unplugin-swc';

/**
 * Vitest 需要 emitDecoratorMetadata 才能让 NestJS 的 DI 在测试中工作，
 * 而默认的 esbuild 转换不产出该元数据，故使用 SWC 转换器。
 */
export default defineConfig({
  test: {
    environment: 'node',
    include: ['test/**/*.test.ts'],
    globals: false,
    // 集成测试会真实拷贝模板目录/起 Nest 应用，并行跑 workspace 时较慢
    testTimeout: 30000,
    hookTimeout: 30000,
  },
  plugins: [
    swc.vite({
      module: { type: 'es6' },
      jsc: {
        target: 'es2021',
        parser: { syntax: 'typescript', decorators: true },
        transform: { legacyDecorator: true, decoratorMetadata: true },
      },
    }),
  ],
});

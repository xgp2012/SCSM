import 'reflect-metadata';
import { NestFactory } from '@nestjs/core';
import { Logger, ValidationPipe } from '@nestjs/common';
import { NestExpressApplication } from '@nestjs/platform-express';
import { WsAdapter } from '@nestjs/platform-ws';
import cookieParser from 'cookie-parser';
import { join } from 'path';
import { existsSync } from 'fs';
import { AppModule } from './app.module';
import { createLogger } from './common/logger/logger';
import { AllExceptionsFilter } from './common/filters/all-exceptions.filter';
import { parseEnv } from './config/env';

async function bootstrap(): Promise<void> {
  const env = parseEnv(process.env);
  const logger = new Logger('Bootstrap');

  const app = await NestFactory.create<NestExpressApplication>(AppModule, {
    bufferLogs: true,
  });

  app.setGlobalPrefix('api');
  app.useWebSocketAdapter(new WsAdapter(app));
  app.use(cookieParser());
  app.enableCors({
    origin: env.PANEL_CORS_ORIGIN,
    credentials: true,
  });
  app.useGlobalPipes(
    new ValidationPipe({
      whitelist: true,
      transform: true,
      forbidNonWhitelisted: false,
    }),
  );
  app.useGlobalFilters(new AllExceptionsFilter());
  app.enableShutdownHooks();

  const publicDir = join(__dirname, 'public');
  if (existsSync(publicDir)) {
    app.useStaticAssets(publicDir);
    logger.log(`Serving static files from ${publicDir}`);
  }

  const pino = createLogger(env);
  logger.log(`SC-Panel API listening target ${env.PANEL_HOST}:${env.PANEL_PORT} (${env.NODE_ENV})`);
  pino.info(
    { host: env.PANEL_HOST, port: env.PANEL_PORT, env: env.NODE_ENV },
    'bootstrap complete',
  );

  await app.listen(env.PANEL_PORT, env.PANEL_HOST);
  logger.log(`API ready at http://${env.PANEL_HOST}:${env.PANEL_PORT}/api/health`);
}

void bootstrap();

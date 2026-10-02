import { Module } from '@nestjs/common';
import { JwtModule, type JwtSignOptions } from '@nestjs/jwt';
import { EnvModule } from '../config/env.module';
import { PANEL_ENV } from '../config/env.module';
import type { PanelEnv } from '../config/env';
import { AuthService } from './auth.service';
import { AuthController } from './auth.controller';

@Module({
  imports: [
    EnvModule,
    JwtModule.registerAsync({
      inject: [PANEL_ENV],
      useFactory: (env: PanelEnv) => ({
        secret: env.JWT_SECRET,
        signOptions: { expiresIn: env.JWT_EXPIRES_IN as JwtSignOptions['expiresIn'] },
      }),
    }),
  ],
  controllers: [AuthController],
  providers: [AuthService],
  exports: [AuthService],
})
export class AuthModule {}

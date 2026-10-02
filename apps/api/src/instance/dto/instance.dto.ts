import { Type } from 'class-transformer';
import {
  IsArray,
  IsBoolean,
  IsIn,
  IsInt,
  IsOptional,
  IsString,
  Max,
  Min,
  MinLength,
} from 'class-validator';
import type { CreateInstanceInput, InstanceSource, UpdateInstanceInput } from '@sc-panel/shared';

export class CreateInstanceDto implements CreateInstanceInput {
  @IsString()
  @MinLength(1)
  name!: string;

  @IsOptional()
  @IsString()
  dir?: string;

  @IsOptional()
  @IsIn(['template', 'upload', 'import', 'version'])
  source?: InstanceSource;

  @IsOptional()
  @IsInt()
  @Min(1)
  @Max(65535)
  serverPort?: number;

  @IsOptional()
  @IsInt()
  @Min(1)
  @Max(65535)
  broadcastPort?: number;

  @IsOptional()
  @IsString()
  version?: string;

  @IsOptional()
  @IsArray()
  @IsString({ each: true })
  launchArgs?: string[];

  @IsOptional()
  @IsString()
  worldName?: string;

  @IsOptional()
  @IsInt()
  @Min(1)
  @Max(1024)
  maxPlayers?: number;

  @IsOptional()
  @IsBoolean()
  autoRestart?: boolean;

  @IsOptional()
  @IsBoolean()
  autoStart?: boolean;

  @IsOptional()
  @IsBoolean()
  autoRun?: boolean;

  @IsOptional()
  @IsString()
  templateDir?: string;
}

export class UpdateInstanceDto implements UpdateInstanceInput {
  @IsOptional()
  @IsString()
  @MinLength(1)
  name?: string;

  @IsOptional()
  @IsInt()
  @Min(1)
  @Max(65535)
  serverPort?: number;

  @IsOptional()
  @IsInt()
  @Min(1)
  @Max(65535)
  broadcastPort?: number;

  @IsOptional()
  @IsString()
  version?: string;

  @IsOptional()
  @IsArray()
  @IsString({ each: true })
  launchArgs?: string[];

  @IsOptional()
  @IsString()
  worldPath?: string;

  @IsOptional()
  @IsString()
  worldName?: string;

  @IsOptional()
  @IsInt()
  @Min(1)
  @Max(1024)
  maxPlayers?: number;

  @IsOptional()
  @IsBoolean()
  autoRestart?: boolean;

  @IsOptional()
  @IsBoolean()
  autoStart?: boolean;

  @IsOptional()
  @IsBoolean()
  autoRun?: boolean;

  @IsOptional()
  @IsBoolean()
  autoGenerateWorld?: boolean;

  @IsOptional()
  @IsString()
  worldSeed?: string;

  @IsOptional()
  @IsInt()
  @Min(0)
  @Max(3)
  gameMode?: number;

  @IsOptional()
  @IsBoolean()
  pvpEnabled?: boolean;

  @IsOptional()
  @IsBoolean()
  seasonChanging?: boolean;

  @IsOptional()
  @IsString()
  worldPassword?: string;
}

/** import 安装上传包：用 Type 保持可选数值转换（此处仅文件名/内容在 controller 处理） */
export class InstallUploadDto {
  @IsOptional()
  @Type(() => String)
  name?: string;
}

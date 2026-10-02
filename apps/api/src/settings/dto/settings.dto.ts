import { Type } from 'class-transformer';
import {
  IsIn,
  IsInt,
  IsOptional,
  IsString,
  MaxLength,
  Min,
  MinLength,
  ValidateNested,
} from 'class-validator';
import type { BackupPolicy, DownloadPolicy, UiPreferences } from '@sc-panel/shared';

class BackupPolicyDto implements Partial<BackupPolicy> {
  @IsOptional() @IsInt() @Min(0) intervalMs?: number;
  @IsOptional() @IsInt() @Min(0) keep?: number;
}

class DownloadPolicyDto implements Partial<DownloadPolicy> {
  @IsOptional() @IsString() @MaxLength(200) giteeMirror?: string;
  @IsOptional() @IsInt() @Min(1) maxBytes?: number;
  @IsOptional() @IsInt() @Min(0) cacheMs?: number;
}

class UiPreferencesDto implements Partial<UiPreferences> {
  @IsOptional() @IsIn(['dark', 'light', 'system']) theme?: UiPreferences['theme'];
}

export class UpdatePanelSettingsDto {
  @IsOptional() @ValidateNested() @Type(() => BackupPolicyDto) backup?: BackupPolicyDto;
  @IsOptional() @ValidateNested() @Type(() => DownloadPolicyDto) download?: DownloadPolicyDto;
  @IsOptional() @ValidateNested() @Type(() => UiPreferencesDto) ui?: UiPreferencesDto;
}

export class ChangePasswordDto {
  @IsString() @MinLength(1) currentPassword!: string;
  @IsString() @MinLength(6) @MaxLength(200) newPassword!: string;
}

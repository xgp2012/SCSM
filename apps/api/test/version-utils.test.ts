import { describe, expect, it } from 'vitest';
import {
  detectArchiveKind,
  safeFilename,
  stripExecutableExt,
  formatBytes,
} from '../src/version/archive-utils';
import {
  detectArch,
  detectPlatform,
  isServerPackageName,
  serverPackageScore,
  toReleaseInfo,
} from '../src/version/version-source';

describe('archive-utils (M6)', () => {
  it('detects archive kind from name/url', () => {
    expect(detectArchiveKind('服务端SCNET.zip')).toBe('zip');
    expect(detectArchiveKind('http://x/y/服务端.tgz?token=1')).toBe('tar.gz');
    expect(detectArchiveKind('readme.txt')).toBe('unknown');
  });

  it('sanitizes filenames against traversal', () => {
    expect(safeFilename('../../etc/passwd')).not.toContain('/');
    expect(safeFilename('..\\..\\win')).not.toContain('\\');
    expect(safeFilename('')).toBe('version');
  });

  it('strips executable extensions for dotnet muxer detection', () => {
    expect(stripExecutableExt('dotnet.EXE')).toBe('dotnet');
    expect(stripExecutableExt('Survivalcraft.dll')).toBe('survivalcraft.dll');
  });

  it('formats byte counts', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1024)).toBe('1.00 KB');
    expect(formatBytes(23 * 1024 * 1024)).toContain('23.00 MB');
  });
});

describe('version-source asset detection (M6)', () => {
  it('recognizes server packages and ignores client/apk/source archives', () => {
    expect(isServerPackageName('[服务端]SCNETx26.06.19z1.zip')).toBe(true);
    expect(isServerPackageName('[服务端][Linux]PocketSurvival.tar.gz')).toBe(true);
    expect(isServerPackageName('[电脑版][Windows]SCNET.zip')).toBe(false);
    expect(isServerPackageName('[安卓版][arm64]SCNET.apk')).toBe(false);
    expect(isServerPackageName('x26.06.19.zip')).toBe(false);
  });

  it('detects platform/arch', () => {
    expect(detectPlatform('[服务端][Linux]app.tar.gz')).toBe('linux');
    expect(detectPlatform('[服务端][Windows]app.zip')).toBe('windows');
    expect(detectPlatform('[服务端]app.zip')).toBe('unknown');
    expect(detectArch('[服务端][linux-arm64]app.zip')).toBe('arm64');
    expect(detectArch('[服务端][Linuxx64]app.zip')).toBe('x64');
  });

  it('ranks managed Survivalcraft server packages above PocketSurvival variants', () => {
    const managed = serverPackageScore('服务端X26.07.01.01.zip');
    const scnet = serverPackageScore('[服务端]SCNETx26.06.19z1.zip');
    const pocket = serverPackageScore('[服务端][Windows]PocketSurvival-d95cf13b.zip');
    const client = serverPackageScore('[电脑版][Windows]SCNET.zip');
    expect(managed).toBeGreaterThan(scnet);
    expect(scnet).toBeGreaterThan(pocket);
    // 客户端包不是服务端包，评分最低
    expect(client).toBe(-1);
    expect(pocket).toBeLessThanOrEqual(client);
  });

  it('maps raw gitee release to shared ReleaseInfo', () => {
    const info = toReleaseInfo({
      tag: 'x26.06.19',
      name: 'x26.06.19',
      assets: [
        { name: '[服务端]SCNETx26.06.19z1.zip', url: 'https://gitee.com/dl/a' },
        { name: '[电脑版][Windows]x.zip', url: 'https://gitee.com/dl/b' },
        { name: 'x26.06.19.zip', url: 'https://gitee.com/archive/refs/tags/x26.06.19.zip' },
      ],
    });
    expect(info.tag).toBe('x26.06.19');
    expect(info.assets[0].isServerPackage).toBe(true);
    expect(info.assets[1].isServerPackage).toBe(false);
    expect(info.assets[2].isSourceArchive).toBe(true);
  });
});

#!/usr/bin/env node
// extract_turing_token.mjs — 从本机 WorkBuddy 客户端自带的 Turing Shield SDK 提取设备令牌。
//
// 背景（2026-10-25 客户端协议逆向）：上游计费/签到类接口校验 X-Device-Token，
// 该令牌由客户端内置的腾讯 TuringShieldSDK（N-API 原生模块）生成，网关容器内
// 无法现取。本脚本在装有 WorkBuddy 客户端的机器上运行一次，产出令牌落盘，
// 配合网关 config upstream.device_token_file（或环境变量 WB2A_DEVICE_TOKEN_FILE）
// 注入到出站请求。令牌有时效，客户端侧会自动刷新——建议定期（如每日签到前的
// cron）重跑本脚本刷新落盘文件。
//
// 用法：
//   node scripts/extract_turing_token.mjs [--sdk-dir <turing-sdk 目录>] [--out <文件>] [--app-version <v>]
//   默认 sdk-dir = C:\Program Files\WorkBuddy\resources\app.asar.unpacked\native\turing-sdk
//   默认 out     = ./device_token（与网关 compose 的 ./data 挂载约定对齐可自选）
//   不带 --out 时仅向 stdout 打印打码令牌（末 4 位），避免明文进日志。
//
// 只读使用客户端自带 SDK，不修改客户端任何文件。

import { createRequire } from 'node:module';
import { existsSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const DEFAULT_SDK_DIR = 'C:/Program Files/WorkBuddy/resources/app.asar.unpacked/native/turing-sdk';
const DEFAULT_CHANNEL_ID = 109144; // 客户端 cli/product.json .config.turingSdk.channelId（实测）
const DEFAULT_APP_VERSION = '5.8.3';

const args = process.argv.slice(2);
function argOf(name, fallback) {
  const i = args.indexOf(name);
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
}

const sdkDir = resolve(argOf('--sdk-dir', DEFAULT_SDK_DIR));
const outPath = argOf('--out', null);
const appVersion = argOf('--app-version', DEFAULT_APP_VERSION);
const indexCjs = join(sdkDir, 'index.cjs');

if (!existsSync(indexCjs)) {
  console.error(`[extract-turing] SDK 不存在：${indexCjs}`);
  console.error('[extract-turing] 请确认本机已安装 WorkBuddy 桌面客户端，或用 --sdk-dir 指定目录。');
  process.exit(2);
}

const require = createRequire(import.meta.url);
let sdk;
try {
  sdk = require(indexCjs);
} catch (err) {
  console.error(`[extract-turing] SDK 加载失败：${err.message}`);
  process.exit(3);
}

if (!sdk.isSupported?.()) {
  console.error(`[extract-turing] 平台不支持或原生模块缺失：${sdk.getLoadError?.() ?? 'unknown'}`);
  process.exit(3);
}

// configure(channelId, productName, productVersion, keychainStorageEnabled)
// 与客户端主进程装配一致（keychain 存储关闭：提取器不需要持久密钥链）。
try {
  sdk.configure(DEFAULT_CHANNEL_ID, 'WorkBuddy', appVersion, false);
} catch (err) {
  console.error(`[extract-turing] configure 失败：${err.message}`);
  process.exit(4);
}

// fetchDeviceToken(options)：includesDeviceInfo 与客户端常规路径一致；超时 15s。
try {
  const result = await Promise.race([
    Promise.resolve(sdk.fetchDeviceToken({ includesDeviceInfo: true, timeoutMs: 15000 })),
    new Promise((_, rej) => setTimeout(() => rej(new Error('fetchDeviceToken 超时（15s）')), 20000)),
  ]);

  // SDK 返回形态兼容：{token|device_token|message, ...} 或裸字符串。
  const token =
    typeof result === 'string'
      ? result
      : result?.token ?? result?.device_token ?? result?.message ?? '';
  if (!token || typeof token !== 'string') {
    console.error(`[extract-turing] 未取得令牌，SDK 返回：${JSON.stringify(result)?.slice(0, 300)}`);
    process.exit(5);
  }

  const masked = `${token.slice(0, 4)}…<len=${token.length}>…${token.slice(-4)}`;
  if (outPath) {
    mkdirSync(dirname(resolve(outPath)), { recursive: true });
    writeFileSync(outPath, token + '\n', { encoding: 'utf8', mode: 0o600 });
    console.log(`[extract-turing] 令牌已写入 ${resolve(outPath)}（${masked}）`);
    console.log('[extract-turing] 网关侧配置 upstream.device_token_file 指向该文件即可（5 分钟内生效）。');
  } else {
    console.log(`[extract-turing] 令牌（打码）：${masked}`);
    console.log('[extract-turing] 加 --out <文件> 落盘给网关使用。');
  }
} catch (err) {
  console.error(`[extract-turing] 获取令牌失败：${err.message}`);
  console.error('[extract-turing] 提示：SDK 可能需要客户端在正常网络下初始化一次；可先启动一次 WorkBuddy 客户端后重试。');
  process.exit(6);
}

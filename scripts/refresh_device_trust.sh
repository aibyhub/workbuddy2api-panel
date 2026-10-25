#!/usr/bin/env bash
# refresh_device_trust.sh — 刷新设备信任两件套并同步到 ll 服务器（配合计划任务每日运行）。
#
# 做什么：
#   1. 用本机 WorkBuddy 客户端自带 Turing SDK 铸出最新 X-Device-Token（有时效，需定期刷新）
#   2. 复制本机客户端的 qimei36 缓存（qimei 为设备级标识，稳定不变，同步属兜底）
#   3. scp 到 ll 服务器 compose 的 data/ 目录并修正属主（容器内 uid 10001 可读）
#
# 网关侧读取：upstream.device_token_file / qimei_file（compose environment 已配置，
# 5 分钟读取缓存——文件更新后最迟 5 分钟生效，无需重启容器）。
# 时效：计划任务每天 08:30 运行（赶在 9:00 签到前）；电脑关机错过则开机补跑需在
# 计划任务设置里勾选「错过后尽快启动」（注册脚本已设置 StartWhenAvailable）。
set -euo pipefail

PROJECT="$(cd "$(dirname "$0")/.." && pwd)"
STAGE="D:/workspace/tmp/wb-device-trust"
REMOTE_DIR="/opt/1panel/docker/compose/workbuddy2api/data"
REMOTE="ll"

mkdir -p "$STAGE"

echo "[refresh] $(date '+%F %T') 铸出新 Turing 令牌..."
node "$PROJECT/scripts/extract_turing_token.mjs" --out "$STAGE/device_token"

cp "C:/Users/aibyt/.workbuddy/qimei-cache.json" "$STAGE/qimei-cache.json"

echo "[refresh] 采集本机真实硬件 specs（与 qimei/令牌同机自洽）..."
powershell -NoProfile -Command "
\$cs = Get-CimInstance Win32_ComputerSystem
\$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
\$osv = (Get-CimInstance Win32_OperatingSystem).Version
\$cores = if (\$cpu.NumberOfLogicalProcessors) { \$cpu.NumberOfLogicalProcessors } else { \$cs.NumberOfProcessors }
\$mem = [math]::Round(\$cs.TotalPhysicalMemory/1GB)
@{ os='win32'; arch='x64'; osVersion=\$osv; cpuCores=[int]\$cores; memorySize=[int]\$mem } | ConvertTo-Json -Compress | Set-Content -NoNewline -Encoding ascii '$(cygpath -w "$STAGE/device-info.json")'
"
cat "$STAGE/device-info.json"; echo

echo "[refresh] 同步到 ${REMOTE}:${REMOTE_DIR} ..."
scp -o BatchMode=yes -q "$STAGE/device_token" "$STAGE/qimei-cache.json" "$STAGE/device-info.json" "$REMOTE:$REMOTE_DIR/"
ssh -o BatchMode=yes "$REMOTE" "chown 10001:10001 $REMOTE_DIR/device_token $REMOTE_DIR/qimei-cache.json $REMOTE_DIR/device-info.json && chmod 600 $REMOTE_DIR/device_token && chmod 644 $REMOTE_DIR/qimei-cache.json $REMOTE_DIR/device-info.json && echo \"[refresh] 服务器侧就绪：device_token=\$(wc -c < $REMOTE_DIR/device_token)B qimei=\$(wc -c < $REMOTE_DIR/qimei-cache.json)B info=\$(wc -c < $REMOTE_DIR/device-info.json)B\""

echo "[refresh] $(date '+%F %T') 完成（网关 5 分钟内自动生效，无需重启）"

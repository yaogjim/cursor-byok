#!/bin/sh
# 卸载后刷新桌面数据库；失败不影响卸载结果。
set -e

if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
fi

exit 0

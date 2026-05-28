"""
一次性工具：產生 Pyrogram session string。
在本機執行（不在 Docker 中），需要互動式輸入手機號碼和驗證碼。

用法：
  pip install pyrogram tgcrypto
  python generate_session.py

預設會讀取同目錄的 .env，亦相容以下兩種變數名稱：
  TELEGRAM_API_ID / TELEGRAM_API_HASH
  API_ID / API_HASH

產生的 session string 放到 .env 的 SESSION_STRING 中。
"""

import os
from pathlib import Path

from pyrogram import Client


def _load_env_file() -> None:
    env_path = Path(__file__).with_name(".env")
    if not env_path.exists():
        return
    for line in env_path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip()
        if key and key not in os.environ:
            os.environ[key] = value


def _require_env(*keys: str) -> str:
    for key in keys:
        value = os.environ.get(key)
        if value:
            return value
    raise RuntimeError(
        f"缺少環境變數：請在 userbot/.env 設定 {', '.join(keys)}，"
        "或先在 shell export 後再執行。"
    )


_load_env_file()
API_ID = int(_require_env("TELEGRAM_API_ID", "API_ID"))
API_HASH = _require_env("TELEGRAM_API_HASH", "API_HASH")

with Client("gen_session", api_id=API_ID, api_hash=API_HASH) as app:
    print("\n=== Session String ===")
    print(app.export_session_string())
    print("=== 複製上面的字串到 .env 的 SESSION_STRING ===\n")

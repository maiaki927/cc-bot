"""
cc-bot Userbot Relay — 監聽 Telegram 群組訊息並寫入 relay 檔案。

使用 Pyrogram (MTProto) 連接個人帳號，能看到所有訊息（包含 bot 發送的）。
將訊息寫入共享的 relay JSON 檔案，供 cc-bot 的 RelayWatcher 讀取並 push 給 Claude Code。
"""

import os
import json
import time
import tempfile
import logging

from pyrogram import Client
from pyrogram.types import Message as PyroMessage

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
)
logger = logging.getLogger("userbot")

# ---- 設定 ----
API_ID = int(os.environ["API_ID"])
API_HASH = os.environ["API_HASH"]
SESSION_STRING = os.environ.get("SESSION_STRING", "")
RELAY_FILE = os.environ.get("RELAY_FILE", "/data/cc-bot-relay.json")
MAX_MESSAGES = int(os.environ.get("MAX_MESSAGES", "50"))
MAX_AGE_SECONDS = int(os.environ.get("MAX_AGE_SECONDS", "3600"))

# 監控的 chat ID（逗號分隔，留空 = 全部）
WATCHED_CHATS_RAW = os.environ.get("WATCHED_CHATS", "")
WATCHED_CHATS = set()
if WATCHED_CHATS_RAW.strip():
    for cid in WATCHED_CHATS_RAW.split(","):
        cid = cid.strip()
        if cid:
            WATCHED_CHATS.add(int(cid))

if WATCHED_CHATS:
    logger.info(f"監控 chat IDs: {WATCHED_CHATS}")
else:
    logger.info("監控所有 chats")

# ---- Pyrogram client ----
if SESSION_STRING:
    app = Client("userbot", api_id=API_ID, api_hash=API_HASH,
                 session_string=SESSION_STRING)
else:
    app = Client("userbot", api_id=API_ID, api_hash=API_HASH,
                 workdir=os.environ.get("SESSION_DIR", "/data/sessions"))


def should_capture(message: PyroMessage) -> bool:
    """判斷是否應該捕獲這則訊息"""
    if not message:
        return False
    if not (message.text or message.caption or message.photo
            or message.document or message.sticker):
        return False
    if WATCHED_CHATS and message.chat.id not in WATCHED_CHATS:
        return False
    return True


def message_to_dict(message: PyroMessage) -> dict:
    """將 Pyrogram Message 轉成與 Go Message struct 對應的 dict"""
    text = message.text or message.caption or ""

    username = ""
    first_name = ""
    user_id = 0
    sender = message.from_user
    if sender:
        username = sender.username or ""
        first_name = sender.first_name or ""
        user_id = sender.id

    result = {
        "message_id": message.id,
        "chat_id": message.chat.id,
        "text": text,
        "username": username,
        "first_name": first_name,
        "user_id": user_id,
        "date": int(message.date.timestamp()) if message.date else int(time.time()),
    }

    if message.photo:
        result["attachment_file_id"] = message.photo.file_id
    if message.document:
        result["attachment_file_id"] = message.document.file_id
    if message.sticker:
        result["text"] = text or f"[sticker: {message.sticker.emoji or '?'}]"

    return result


def read_relay_file() -> list:
    """安全讀取 relay 檔案"""
    try:
        with open(RELAY_FILE, "r", encoding="utf-8") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return []


def write_relay_file(messages: list):
    """原子性寫入 relay 檔案"""
    relay_dir = os.path.dirname(RELAY_FILE) or "."
    tmp_path = None
    try:
        fd, tmp_path = tempfile.mkstemp(dir=relay_dir, suffix=".tmp")
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(messages, f, ensure_ascii=False)
        os.replace(tmp_path, RELAY_FILE)
        tmp_path = None  # rename 成功，不需清理
    except Exception as e:
        logger.error(f"寫入 relay 失敗: {e}")
    finally:
        if tmp_path:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass


def append_message(msg_dict: dict):
    """讀取現有訊息 → 附加新訊息 → 裁剪 → 原子性寫回"""
    msgs = read_relay_file()

    # 去重
    key = (msg_dict["chat_id"], msg_dict["message_id"])
    for existing in msgs:
        if (existing["chat_id"], existing["message_id"]) == key:
            return

    msgs.append(msg_dict)

    # 裁剪：移除過舊的訊息
    cutoff = int(time.time()) - MAX_AGE_SECONDS
    msgs = [m for m in msgs if m.get("date", 0) >= cutoff]

    # 裁剪：保留最新 MAX_MESSAGES 則
    if len(msgs) > MAX_MESSAGES:
        msgs = msgs[-MAX_MESSAGES:]

    write_relay_file(msgs)
    logger.info(
        f"relay: msg_id={msg_dict['message_id']} "
        f"chat={msg_dict['chat_id']} "
        f"user={msg_dict.get('username', '?')}"
    )


@app.on_message()
async def handler(client, message: PyroMessage):
    if not should_capture(message):
        return
    msg_dict = message_to_dict(message)
    append_message(msg_dict)


if __name__ == "__main__":
    logger.info("Userbot relay 啟動中...")
    app.run()

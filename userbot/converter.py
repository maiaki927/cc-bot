"""Pyrogram Message → dict 轉換。"""

import time

from pyrogram.types import Message as PyroMessage


def should_capture(message: PyroMessage, watched_chats: set[int]) -> bool:
    """判斷是否應該捕獲這則訊息。"""
    if not message:
        return False
    if not (message.text or message.caption or message.photo
            or message.document or message.sticker):
        return False
    if watched_chats and message.chat.id not in watched_chats:
        return False
    return True


def message_to_dict(message: PyroMessage) -> dict:
    """將 Pyrogram Message 轉成與 Go Message struct 對應的 dict。"""
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
    elif message.document:
        result["attachment_file_id"] = message.document.file_id
    if message.sticker and not text:
        result["text"] = f"[sticker: {message.sticker.emoji or '?'}]"

    return result

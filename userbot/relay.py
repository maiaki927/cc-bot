"""Relay file 讀寫、裁剪與原子性寫入。"""

import json
import logging
import os
import tempfile
import time

logger = logging.getLogger("userbot")


def read_relay(path: str) -> list[dict]:
    """安全讀取 relay 檔案。"""
    try:
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return []


def write_relay(path: str, messages: list[dict]) -> None:
    """原子性寫入 relay 檔案。"""
    relay_dir = os.path.dirname(path) or "."
    tmp_path = None
    try:
        fd, tmp_path = tempfile.mkstemp(dir=relay_dir, suffix=".tmp")
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(messages, f, ensure_ascii=False)
        os.replace(tmp_path, path)
        tmp_path = None
    except Exception:
        logger.exception("寫入 relay 失敗")
    finally:
        if tmp_path:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass


def append_message(
    path: str,
    msg_dict: dict,
    max_messages: int,
    max_age_seconds: int,
) -> None:
    """讀取現有訊息 → 去重 → 附加 → 裁剪 → 原子性寫回。"""
    msgs = read_relay(path)

    # 去重
    key = (msg_dict["chat_id"], msg_dict["message_id"])
    for existing in msgs:
        if (existing["chat_id"], existing["message_id"]) == key:
            return

    msgs.append(msg_dict)

    # 裁剪：移除過舊的訊息
    cutoff = int(time.time()) - max_age_seconds
    msgs = [m for m in msgs if m.get("date", 0) >= cutoff]

    # 裁剪：保留最新 max_messages 則
    if len(msgs) > max_messages:
        msgs = msgs[-max_messages:]

    write_relay(path, msgs)
    logger.info(
        "relay: msg_id=%s chat=%s user=%s",
        msg_dict["message_id"],
        msg_dict["chat_id"],
        msg_dict.get("username", "?"),
    )

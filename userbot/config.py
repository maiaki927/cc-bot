"""環境變數載入與驗證。"""

import os
import logging
from dataclasses import dataclass, field

logger = logging.getLogger("userbot")


@dataclass
class Config:
    api_id: int
    api_hash: str
    session_string: str
    relay_file: str
    watched_chats: set[int] = field(default_factory=set)
    max_messages: int = 50
    max_age_seconds: int = 3600


def load() -> Config:
    """從環境變數載入設定，缺少必填欄位時拋出 ValueError。"""
    raw_api_id = os.environ.get("API_ID", "")
    if not raw_api_id:
        raise ValueError("API_ID is required")

    api_hash = os.environ.get("API_HASH", "")
    if not api_hash:
        raise ValueError("API_HASH is required")

    watched = set()
    raw = os.environ.get("WATCHED_CHATS", "").strip()
    if raw:
        for cid in raw.split(","):
            cid = cid.strip()
            if cid:
                watched.add(int(cid))

    cfg = Config(
        api_id=int(raw_api_id),
        api_hash=api_hash,
        session_string=os.environ.get("SESSION_STRING", ""),
        relay_file=os.environ.get("RELAY_FILE", "/data/cc-bot-relay.json"),
        watched_chats=watched,
        max_messages=int(os.environ.get("MAX_MESSAGES", "50")),
        max_age_seconds=int(os.environ.get("MAX_AGE_SECONDS", "3600")),
    )

    if cfg.watched_chats:
        logger.info("監控 chat IDs: %s", cfg.watched_chats)
    else:
        logger.info("監控所有 chats")

    return cfg

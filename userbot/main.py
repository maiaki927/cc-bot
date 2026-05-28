"""cc-bot Userbot Relay — 監聽 Telegram 群組訊息並寫入 relay 檔案。

使用 Pyrogram (MTProto) 連接個人帳號，能看到所有訊息（包含 bot 發送的）。
將訊息寫入共享的 relay JSON 檔案，供 cc-bot 的 RelayWatcher 讀取並 push 給 Claude Code。
"""

import logging
import os
import threading

from pyrogram import Client
from pyrogram.types import Message as PyroMessage

from config import Config, load
from converter import message_to_dict, should_capture
from relay import append_message

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
)
logger = logging.getLogger("userbot")


def create_app(cfg: Config) -> Client:
    if cfg.session_string:
        app = Client(
            "userbot",
            api_id=cfg.api_id,
            api_hash=cfg.api_hash,
            session_string=cfg.session_string,
        )
    else:
        app = Client(
            "userbot",
            api_id=cfg.api_id,
            api_hash=cfg.api_hash,
            workdir=os.environ.get("SESSION_DIR", "/data/sessions"),
        )

    # Lock to prevent race condition on relay file read-modify-write.
    relay_lock = threading.Lock()

    @app.on_message()
    async def handler(_client: Client, message: PyroMessage) -> None:
        if not should_capture(message, cfg.watched_chats):
            return
        msg_dict = message_to_dict(message)
        with relay_lock:
            append_message(cfg.relay_file, msg_dict, cfg.max_messages, cfg.max_age_seconds)

    return app


def main() -> None:
    cfg = load()
    app = create_app(cfg)
    logger.info("Userbot relay 啟動中...")
    app.run()


if __name__ == "__main__":
    main()

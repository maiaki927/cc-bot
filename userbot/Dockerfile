FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .

# tgcrypto 需從原始碼編譯；slim 映像不含 gcc
# 安裝、編譯、清理合併在同一 RUN 層以縮小 image
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential \
    && pip install --no-cache-dir -r requirements.txt \
    && apt-get purge -y --auto-remove build-essential \
    && rm -rf /var/lib/apt/lists/*

COPY config.py converter.py relay.py main.py ./

CMD ["python", "-u", "main.py"]

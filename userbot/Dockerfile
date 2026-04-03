FROM python:3.12-slim

WORKDIR /app

# tgcrypto 需從原始碼編譯；slim 映像不含 gcc
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt \
    && apt-get purge -y --auto-remove build-essential

COPY config.py converter.py relay.py main.py ./

CMD ["python", "-u", "main.py"]

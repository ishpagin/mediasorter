FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 && rm -rf /var/lib/apt/lists/*
COPY app/requirements.txt /tmp/requirements.txt
# playwright is only for UI tests on the dev machine
RUN grep -v '^playwright' /tmp/requirements.txt > /tmp/req.txt && pip install --no-cache-dir -r /tmp/req.txt
WORKDIR /app

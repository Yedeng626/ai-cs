FROM python:3.11-slim

WORKDIR /app

RUN pip install --no-cache-dir --default-timeout=120 \
    -i https://mirrors.cloud.tencent.com/pypi/simple/ \
    --trusted-host mirrors.cloud.tencent.com \
    sentence-transformers==2.7.0 \
    fastapi==0.115.0 \
    uvicorn[standard]==0.30.0

COPY serve.py /app/

# Model mount point — model files expected at /app/model/
ENV SENTENCE_TRANSFORMERS_HOME=/app/model

EXPOSE 8080

CMD ["python", "serve.py"]

"""
BGE Embedding Service — compatible with AI-CS custom embedding API.
Provides /v1/embeddings endpoint.

Model loaded from local files only — no network access needed.
"""
import os
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
import uvicorn

MODEL_NAME = os.getenv("BGE_MODEL", "BAAI/bge-small-zh-v1.5")
MODEL_PATH = os.getenv("MODEL_PATH", "/app/model/BAAI/bge-small-zh-v1.5")
PORT = int(os.getenv("PORT", "8088"))

# Force offline — no network
os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["TRANSFORMERS_OFFLINE"] = "1"

print(f"Loading model from: {MODEL_PATH}")
print(f"Model name: {MODEL_NAME}")

# Verify model files exist
required = ["config.json", "tokenizer.json", "pytorch_model.bin"]
for f in required:
    fp = os.path.join(MODEL_PATH, f)
    if not os.path.exists(fp):
        print(f"ERROR: missing {fp}")
        raise FileNotFoundError(f"Missing model file: {fp}")
    print(f"  OK: {f} ({os.path.getsize(fp)} bytes)")

from sentence_transformers import SentenceTransformer

model = SentenceTransformer(MODEL_PATH)
print(f"Model loaded. dim={model.get_sentence_embedding_dimension()}")

app = FastAPI(title="BGE Embedding Service")


class EmbeddingRequest(BaseModel):
    input: str | list[str]
    model: str = ""


@app.post("/v1/embeddings")
async def embeddings(req: EmbeddingRequest):
    texts = [req.input] if isinstance(req.input, str) else req.input
    if not texts:
        raise HTTPException(status_code=400, detail="input is required")
    vectors = model.encode(texts, normalize_embeddings=True)
    data = []
    for i, vec in enumerate(vectors):
        data.append({
            "object": "embedding",
            "index": i,
            "embedding": vec.tolist(),
        })
    return {
        "object": "list",
        "data": data,
        "model": MODEL_NAME,
        "usage": {"prompt_tokens": sum(len(t) for t in texts), "total_tokens": sum(len(t) for t in texts)},
    }


@app.get("/health")
async def health():
    return {"status": "ok", "model": MODEL_NAME, "dim": model.get_sentence_embedding_dimension()}


# HuggingFace Inference API format — what AI-CS built-in BGE client sends
class HFTextsRequest(BaseModel):
    inputs: list[str]


@app.post("/embeddings")
async def embeddings_hf(req: HFTextsRequest):
    """HuggingFace Inference API format: {inputs:[...]} -> [[...]]  """
    vectors = model.encode(req.inputs, normalize_embeddings=True)
    return vectors.tolist()


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=PORT)

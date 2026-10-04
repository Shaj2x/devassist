"""`python -m devassist_api` runs the server (used by the Docker image)."""

import os

import uvicorn

from devassist_api.main import create_app

if __name__ == "__main__":
    uvicorn.run(
        create_app(),
        host="0.0.0.0",  # noqa: S104  (binding all interfaces inside the container is intended)
        port=int(os.environ.get("PORT", "8000")),
        log_config=None,  # keep our JSON logging
    )

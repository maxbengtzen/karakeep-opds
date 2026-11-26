"""Records the Python original's responses (yazdipour/karakeep-opds) as golden files.

Runs in the original's image, with Karakeep and remote images faked from
testdata/fixtures/routes.json. See scripts/golden/record.sh.
"""

import base64
import json
import os
import sys
import zipfile
from datetime import UTC, datetime
from io import BytesIO
from pathlib import Path
from urllib.parse import parse_qsl

import httpx
from starlette.testclient import TestClient

FIX = Path(sys.argv[1])
OUT = Path(sys.argv[2])
KK = FIX / "karakeep"

FIXED_NOW = datetime(2026, 1, 2, 3, 4, 5, 678901, tzinfo=UTC)


class FixedDatetime(datetime):
    @classmethod
    def now(cls, tz=None):
        return FIXED_NOW


import karakeep_opds.app as appmod  # noqa: E402
import karakeep_opds.models as models  # noqa: E402
import karakeep_opds.render as render  # noqa: E402

models.datetime = FixedDatetime
render.datetime = FixedDatetime

routes = json.loads((FIX / "routes.json").read_text())
cases = json.loads((FIX / "cases.json").read_text())
requests_log: list[dict] = []


def karakeep_handler(request: httpx.Request) -> httpx.Response:
    query = dict(parse_qsl(request.url.query.decode(), keep_blank_values=True))
    requests_log.append(
        {
            "path": request.url.path,
            "query": query,
            "authorization": request.headers.get("authorization"),
        }
    )
    for route in routes["karakeep"]:
        if route["path"] == request.url.path and route["query"] == query:
            body = (KK / route["file"]).read_bytes() if "file" in route else route["body"].encode()
            return httpx.Response(
                route.get("status", 200),
                content=body,
                headers={"content-type": route.get("type", "application/json")},
            )
    return httpx.Response(404, json={"code": "NOT_FOUND"})


async def fake_fetch_remote_image(url: str):
    requests_log.append({"remote": url})
    entry = routes["remote"].get(url)
    if not entry:
        return None
    return (KK / entry["file"]).read_bytes(), entry["type"]


appmod.fetch_remote_image = fake_fetch_remote_image

AUTH = {
    "ok": "Basic " + base64.b64encode(b"reader:secret").decode(),
    "bad": "Basic " + base64.b64encode(b"reader:wrong").decode(),
}

OUT.mkdir(parents=True, exist_ok=True)
with TestClient(appmod.app) as client:
    appmod.app.state.http_client = httpx.AsyncClient(
        base_url="http://karakeep.test/api/v1", transport=httpx.MockTransport(karakeep_handler)
    )
    for case in cases:
        requests_log.clear()
        headers = {}
        auth = case.get("auth", "ok")
        if auth in AUTH:
            headers["Authorization"] = AUTH[auth]
        r = client.get(case["path"], headers=headers)
        meta = {
            "status": r.status_code,
            "content_type": r.headers.get("content-type"),
            "content_disposition": r.headers.get("content-disposition"),
            "www_authenticate": r.headers.get("www-authenticate"),
            "upstream": list(requests_log),
        }
        name = case["name"]
        (OUT / f"{name}.meta.json").write_text(json.dumps(meta, indent=2, ensure_ascii=False) + "\n")
        if case.get("epub") and r.status_code == 200:
            target = OUT / f"{name}.epub.d"
            with zipfile.ZipFile(BytesIO(r.content)) as zf:
                (OUT / f"{name}.epub.list").write_text(
                    "".join(
                        f"{i.filename} {'stored' if i.compress_type == 0 else 'deflated'}\n"
                        for i in zf.infolist()
                    )
                )
                zf.extractall(target)
        else:
            (OUT / f"{name}.body").write_bytes(r.content)
        print(name, r.status_code)

import base64
import json
import os
import re
import subprocess
import sys
import time
import uuid
from pathlib import Path


BASE = "https://grok.com"
CHAT = BASE + "/rest/app-chat/conversations/new"
UPLOAD = BASE + "/rest/app-chat/upload-file"
MEDIA_CREATE = BASE + "/rest/media/post/create"
MEDIA_GET = BASE + "/rest/media/post/get"


def env(name: str, default: str = "") -> str:
    return os.environ.get(name, default).strip()


TOKEN = env("GROK_TOKEN")
PROXY = env("GROK_PROXY")
REF_FILE = env("GROK_REF_FILE", "/root/apps/gpt2api/tmp_probe_ref.png")
PROMPT = env("GROK_PROMPT", "test image to video")
TIMEOUT = int(env("GROK_TIMEOUT", "18") or "18")
QUERY_ONLY_POST = env("GROK_QUERY_POST_ID")
UA = env("GROK_UA")
EXTRA_COOKIES = env("GROK_EXTRA_COOKIES")
CF = env("GROK_CF")


def build_cookie(token: str) -> str:
    cookie = f"sso={token}; sso-rw={token}"
    if CF and "cf_clearance=" not in cookie:
        cookie += f"; cf_clearance={CF}"
    if EXTRA_COOKIES:
        for part in EXTRA_COOKIES.split(";"):
            part = part.strip()
            if not part:
                continue
            name = part.split("=", 1)[0].strip()
            if name and (name + "=") not in cookie:
                cookie += "; " + part
    return cookie


def base_headers(accept: str = "*/*") -> list[str]:
    return [
        "-H",
        f"Accept: {accept}",
        "-H",
        "Accept-Language: zh-CN,zh;q=0.9,en;q=0.8",
        "-H",
        "Accept-Encoding: gzip",
        "-H",
        "Cache-Control: no-cache",
        "-H",
        "Pragma: no-cache",
        "-H",
        f"Cookie: {build_cookie(TOKEN)}",
        "-H",
        f"Origin: {BASE}",
        "-H",
        f"Referer: {BASE}/",
        "-H",
        f"User-Agent: {UA}",
        "-H",
        'Sec-Ch-Ua: "Google Chrome";v="142", "Chromium";v="142", "Not(A:Brand";v="24"',
        "-H",
        "Sec-Ch-Ua-Arch: x86",
        "-H",
        "Sec-Ch-Ua-Bitness: 64",
        "-H",
        "Sec-Ch-Ua-Mobile: ?0",
        "-H",
        "Sec-Ch-Ua-Model: ",
        "-H",
        'Sec-Ch-Ua-Platform: "Linux"',
        "-H",
        "Sec-Fetch-Dest: empty",
        "-H",
        "Sec-Fetch-Mode: cors",
        "-H",
        "Sec-Fetch-Site: same-origin",
        "-H",
        "X-Statsig-ID: "
        + base64.b64encode(
            b"e:TypeError: Cannot read properties of undefined (reading 'childNodes')"
        ).decode(),
        "-H",
        f"X-XAI-Request-ID: {uuid.uuid4()}",
    ]


def curl_json(url: str, payload: dict, accept: str = "*/*", timeout: int = 60) -> tuple[int, str]:
    data = json.dumps(payload, ensure_ascii=False)
    cmd = [
        "curl",
        "--silent",
        "--show-error",
        "--compressed",
        "--max-time",
        str(timeout),
    ]
    if PROXY:
        cmd += ["--proxy", PROXY]
    cmd += base_headers(accept)
    cmd += ["-H", "Content-Type: application/json", "-X", "POST", "--data-binary", "@-", url, "-w", "\nHTTPSTATUS:%{http_code}"]
    proc = subprocess.run(cmd, input=data, text=True, capture_output=True)
    output = proc.stdout or ""
    if proc.returncode not in (0, 28):
        raise RuntimeError(proc.stderr.strip() or f"curl exit {proc.returncode}")
    if "\nHTTPSTATUS:" in output:
        body, _, tail = output.rpartition("\nHTTPSTATUS:")
        try:
            code = int(tail.strip())
        except ValueError:
            code = 0
        return code, body
    return 0, output


def snippet(text: str, limit: int = 240) -> str:
    return text.replace("\n", " ")[:limit]


def first_string_by_keys(obj, keys) -> str:
    if isinstance(obj, dict):
        for key in keys:
            value = obj.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
        for value in obj.values():
            found = first_string_by_keys(value, keys)
            if found:
                return found
    elif isinstance(obj, list):
        for value in obj:
            found = first_string_by_keys(value, keys)
            if found:
                return found
    return ""


def collect_video_urls(value, out: list[str]) -> None:
    if isinstance(value, dict):
        for key in ("videoUrl", "videoURL", "video_url", "mediaUrl", "result_url"):
            item = value.get(key)
            if isinstance(item, str) and (
                "mp4" in item or "master" in item or "video" in item
            ):
                out.append(item)
        for child in value.values():
            collect_video_urls(child, out)
    elif isinstance(value, list):
        for child in value:
            collect_video_urls(child, out)
    elif isinstance(value, str):
        if "mp4" in value or "imagine-public.x.ai" in value or "assets.grok.com" in value:
            out.append(value)


def best_video_url(obj) -> str:
    candidates: list[str] = []
    collect_video_urls(obj, candidates)
    deduped: list[str] = []
    for item in candidates:
        if item not in deduped:
            deduped.append(item)
    if not deduped:
        return ""

    def score(item: str) -> int:
        lower = item.lower().strip()
        score_value = 0
        if "master" in lower:
            score_value += 100
        if "original" in lower:
            score_value += 90
        if "source" in lower:
            score_value += 80
        if "download" in lower:
            score_value += 70
        if "1080" in lower or "1920" in lower:
            score_value += 60
        if "720" in lower or "1280" in lower:
            score_value += 40
        if lower.endswith(".mp4"):
            score_value += 20
        return score_value

    return max(deduped, key=score)


def upload_ref() -> dict:
    raw = Path(REF_FILE).read_bytes()
    payload = {
        "fileName": "image.png",
        "fileMimeType": "image/png",
        "content": base64.b64encode(raw).decode(),
    }
    status, body = curl_json(UPLOAD, payload, timeout=120)
    result = {"stage": "upload", "status_code": status, "body": snippet(body, 500)}
    if status // 100 != 2:
        return result
    obj = json.loads(body)
    result["file_id"] = first_string_by_keys(obj, ["fileMetadataId", "file_id", "fileId", "id"])
    result["asset_url"] = first_string_by_keys(
        obj, ["fileUri", "file_uri", "fileUrl", "url", "mediaUrl"]
    )
    if result["asset_url"] and not result["asset_url"].startswith("http"):
        asset_url = result["asset_url"].lstrip("/")
        result["asset_url"] = "https://assets.grok.com/" + asset_url
    return result


def create_parent_post(asset_url: str) -> dict:
    payload = {"mediaType": "MEDIA_POST_TYPE_IMAGE", "prompt": "", "mediaUrl": asset_url}
    status, body = curl_json(MEDIA_CREATE, payload, timeout=60)
    result = {"stage": "parent_post", "status_code": status, "body": snippet(body, 500)}
    if status // 100 != 2:
        return result
    obj = json.loads(body)
    result["post_id"] = first_string_by_keys(obj, ["postId", "id", "mediaPostId"])
    return result


def submit_video(parent_post_id: str, asset_url: str) -> dict:
    message = (asset_url + "  " + PROMPT).strip() + " --mode=custom"
    payload = {
        "deviceEnvInfo": {
            "darkModeEnabled": False,
            "devicePixelRatio": 2,
            "screenHeight": 1329,
            "screenWidth": 2056,
            "viewportHeight": 1083,
            "viewportWidth": 2056,
        },
        "disableMemory": True,
        "disableSearch": True,
        "disableSelfHarmShortCircuit": False,
        "disableTextFollowUps": False,
        "enableImageGeneration": True,
        "enableImageStreaming": True,
        "enableSideBySide": True,
        "fileAttachments": [],
        "forceConcise": False,
        "forceSideBySide": False,
        "imageAttachments": [],
        "imageGenerationCount": 2,
        "isAsyncChat": False,
        "isReasoning": False,
        "message": message,
        "modelMode": "custom",
        "modelName": "grok-3",
        "responseMetadata": {
            "requestModelDetails": {"modelId": "grok-3"},
            "modelConfigOverride": {
                "modelMap": {
                    "videoGenModelConfig": {
                        "parentPostId": parent_post_id,
                        "videoLength": 6,
                        "isVideoEdit": False,
                        "mode": "custom",
                        "originalPrompt": PROMPT,
                        "aspectRatio": "9:16",
                        "resolutionName": "720p",
                    }
                }
            },
        },
        "returnImageBytes": False,
        "returnRawGrokInXaiRequest": False,
        "sendFinalMetadata": True,
        "temporary": True,
        "toolOverrides": {"videoGen": True},
        "enable420": False,
    }
    status, body = curl_json(
        CHAT,
        payload,
        accept="text/event-stream, application/json, */*",
        timeout=TIMEOUT,
    )
    result = {"stage": "conversation", "status_code": status, "stream_excerpt": snippet(body, 900)}
    post_id = ""
    video_url = ""
    thumb_url = ""
    for raw_line in body.splitlines():
        line = raw_line.strip()
        if line.startswith("data:"):
            line = line[5:].strip()
        if not line or line == "[DONE]":
            continue
        try:
            obj = json.loads(line)
        except Exception:
            continue
        current_post_id = first_string_by_keys(
            obj, ["videoPostId", "video_post_id", "postId", "post_id", "mediaPostId", "media_post_id"]
        )
        if current_post_id:
            post_id = current_post_id
        current_video = best_video_url(obj)
        if current_video:
            video_url = current_video
            if not post_id:
                match = re.search(r"([0-9a-fA-F-]{36})", current_video)
                if match:
                    post_id = match.group(1)
        current_thumb = first_string_by_keys(obj, ["thumbnailImageUrl", "thumbnailUrl", "coverUrl"])
        if current_thumb:
            thumb_url = current_thumb
    result["post_id"] = post_id
    result["video_url"] = video_url
    result["thumb_url"] = thumb_url
    return result


def query_post(post_id: str, rounds: int = 6, sleep_sec: int = 6) -> list[dict]:
    rows = []
    for index in range(rounds):
        status, body = curl_json(MEDIA_GET, {"id": post_id}, accept="application/json, */*", timeout=60)
        item = {"round": index + 1, "status_code": status, "body": snippet(body, 500)}
        if status // 100 == 2:
            try:
                obj = json.loads(body)
            except Exception:
                obj = {}
            item["post_id"] = first_string_by_keys(obj, ["postId", "id", "mediaPostId"]) or post_id
            item["video_url"] = best_video_url(obj)
            item["thumb_url"] = first_string_by_keys(obj, ["thumbnailImageUrl", "thumbnailUrl", "coverUrl"])
            item["state"] = first_string_by_keys(obj, ["status", "state"])
        rows.append(item)
        if item.get("video_url"):
            break
        time.sleep(sleep_sec)
    return rows


def main() -> None:
    if not TOKEN:
        raise SystemExit("missing GROK_TOKEN")
    report = {"proxy": PROXY, "query_only_post": QUERY_ONLY_POST}
    if QUERY_ONLY_POST:
        report["queries"] = query_post(QUERY_ONLY_POST, rounds=8, sleep_sec=8)
    else:
        report["upload"] = upload_ref()
        if report["upload"].get("asset_url"):
            report["parent_post"] = create_parent_post(report["upload"]["asset_url"])
        if report.get("parent_post", {}).get("post_id"):
            report["submit"] = submit_video(
                report["parent_post"]["post_id"], report["upload"]["asset_url"]
            )
            post_id = report["submit"].get("post_id") or report["parent_post"].get("post_id")
            if post_id:
                report["queries"] = query_post(post_id, rounds=4, sleep_sec=5)
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()

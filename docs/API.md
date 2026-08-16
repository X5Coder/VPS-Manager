# VPS Manager API

One token controls **every room**. Tokens → Create token.

- **Copy API** = `BASE` + `TOKEN`
- **Copy single script** = GitHub Action: `docker save` then POST `/upload` and **exit** (room updates in the panel)
- **Copy multi script** = GitHub Action: pack compose + images, POST `/upload` and exit
- **Copy prompt** = full AI brief (all commands, responses, errors, both YAMLs)

Auth: `Authorization: Bearer YOUR_TOKEN`  
Errors: `{ "ok": false, "error": "...", "code": "..." }` plus HTTP status.

## 1. Available disk — call this first

```bash
curl -sS "http://YOUR_VPS_IP:9090/api/v1/quota" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**200** `{ quota_available_gb, quota_available, disk_total, disk_used, disk_free, quota_reserved, hint }`  
**401** `{ "error": "unauthorized" }`

`quota_gb` on create must be **> 0** and **≤ quota_available_gb**. Same numbers: `GET /api/v1/storage`.

## 2. List / get

```bash
curl -sS "http://YOUR_VPS_IP:9090/api/v1/projects" -H "Authorization: Bearer YOUR_TOKEN"
curl -sS "http://YOUR_VPS_IP:9090/api/v1/projects/ROOM_ID" -H "Authorization: Bearer YOUR_TOKEN"
```

**200** list: `{ projects: [...], storage }` — each room has `id`, `kind` (`single`|`multi`), `status`, `quota_gb`, `containers`, `images`, `volumes`.  
**404** `{ "error": "not found" }`

## 3. Create empty room

```bash
curl -sS -X POST "http://YOUR_VPS_IP:9090/api/v1/projects" \
  -H "Authorization: Bearer YOUR_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-app","quota_gb":10,"password":"secret6+","kind":"single"}'
```

**200** `{ ok, empty:true, status:"empty", password, project: { id: ROOM_ID } }`  
**400** `quota_required` · `quota_exceeds_available` (includes `quota_available_gb`) · `password_required` · `password_invalid` · `invalid_request`

## 4. Update a room

Same call for the **first image** and every later update. Send the file. **200 = received** — the room updates in the panel. Do not wait in CI for docker load.

`POST /api/v1/projects/ROOM_ID/upload` field name `file`.

One image (`docker save`):

```bash
docker save -o app.tar IMAGE:TAG
curl -fS -H "Authorization: Bearer YOUR_TOKEN" \
  -F "file=@app.tar" \
  "http://YOUR_VPS_IP:9090/api/v1/projects/ROOM_ID/upload"
```

Compose stack (`compose.yml` + `images/*.tar`):

```bash
tar -czf stack.tar.gz compose.yml images
curl -fS -H "Authorization: Bearer YOUR_TOKEN" \
  -F "file=@stack.tar.gz" \
  "http://YOUR_VPS_IP:9090/api/v1/projects/ROOM_ID/upload"
```

The API inspects the archive. Optional `-F container_id=CONTAINER_ID` updates one container in a multi room.

**400** `package_empty` · `package_invalid` · `package_kind_mismatch` · `content_type` · `file_required`  
**404** container not found · **409** deploy already running

## 5. Logs

There is **no** combined log for every container in a room. Pick one container, or read the whole VPS.

**By container name**

```bash
curl -sS "http://YOUR_VPS_IP:9090/api/v1/projects/ROOM_ID/logs?name=auth" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**By container ID** (also docker id or service name)

```bash
curl -sS "http://YOUR_VPS_IP:9090/api/v1/projects/ROOM_ID/logs?container=CONTAINER_ID" \
  -H "Authorization: Bearer YOUR_TOKEN"
```

Same result: `GET /api/v1/projects/ROOM_ID/containers/CONTAINER_ID/logs`

**200** `{ log, container_id, name, containers }`  
**400** `logs_target_required` if both `name` and `container` are missing (single and multi).  
Stream: `GET .../logs?container=ID&stream=1` or `.../logs/stream`  
Clear: `DELETE .../logs?container=ID` or `POST .../logs/clear?container=ID`

Optional `?kind=vps` (default) or `host` | `panel` | `api` | `deploy`.

## 6. .env

```
GET    /api/v1/projects/ROOM_ID/env
GET    /api/v1/projects/ROOM_ID/env?key=API_KEY
POST   /api/v1/projects/ROOM_ID/env   {"key":"API_KEY","value":"..."}
POST   /api/v1/projects/ROOM_ID/env   {"variables":[{"key":"A","value":"1"},{"key":"B","value":"2"}]}
DELETE /api/v1/projects/ROOM_ID/env?key=API_KEY
```

## 7. Images / volumes / compose / terminal / status / agent

```
GET  /api/v1/projects/ROOM_ID/images
POST /api/v1/projects/ROOM_ID/images/load   field: file  (docker load, does not recreate containers)
GET  /api/v1/projects/ROOM_ID/volumes
POST /api/v1/projects/ROOM_ID/volumes   {"name":"data"}
GET  /api/v1/projects/ROOM_ID/volumes/VOLUME_ID
POST /api/v1/projects/ROOM_ID/volumes/VOLUME_ID/clean
DELETE /api/v1/projects/ROOM_ID/volumes/VOLUME_ID
GET  /api/v1/projects/ROOM_ID/compose
GET  /api/v1/projects/ROOM_ID/compose/validate
GET  /api/v1/projects/ROOM_ID/compose/analyze
POST /api/v1/projects/ROOM_ID/stack/start|stop|restart|remove
POST /api/v1/projects/ROOM_ID/exec   {"command":"ls -la"}
  waits until the command finishes. stdout, stderr, exit_code. No short timeout.
  container_id optional (required only if the room has more than one container and you want a specific one).
GET  /api/v1/projects/ROOM_ID/terminal/ws?access_token=TOKEN   interactive websocket
GET  /api/v1/quota     GET only (POST → 405)
GET  /api/v1/status   VPS + per-room storage (CPU/RAM per room is not live docker stats)
POST /api/v1/agent    {"tool":"list_rooms"}
POST /api/v1/agent/chat  {"messages":[{"role":"user","text":"..."}]}
```

Create empty room also accepts `generate_password`, `domain`, `ssl`, `ssh_certificate`.

## 8. Files (panel)

Containers tab → click a container → Files / Logs / Update image.  
Volumes tab → click a volume → browse files inside it.

```
GET /api/rooms/ROOM_ID/containers/CONTAINER_ID/files?path=/
GET /api/rooms/ROOM_ID/volumes/VOLUME_ID/files?path=/
GET /api/v1/projects/ROOM_ID/containers/CONTAINER_ID/files?path=/
GET /api/v1/projects/ROOM_ID/volumes/VOLUME_ID/files?path=/
```

Bearer API token is accepted on these paths (same as `/api/v1`). Invalid token → **401** `invalid api token`. Invalid `path` → **400**.

## 9. GitHub Actions

The Action **builds or packs**, then POSTs to the API. Use **one** workflow:

| Kind | File | What it sends |
| --- | --- | --- |
| Single | `.github/workflows/vps-deploy-single.yml` | `app.tar` |
| Multi | `.github/workflows/vps-deploy-multi.yml` | `project.vps.tar.gz` |

Copy the matching script from Tokens. Set `ROOM_ID`. Repo **PRIVATE**.  
Single Action fails if `compose.yml` or `images/*.tar` exist. Multi Action fails if `.yml` or `images/*.tar` is missing.

## Backup

See panel Restore. Layout: images on GitHub Releases, containers repo, volume repos (4GiB cap).

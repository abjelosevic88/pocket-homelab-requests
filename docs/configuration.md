# Configuration

Environment variables; every one has a default. Personal settings (formats, notifications, AI) live in the UI per person — `OPENAI_*`/`NOTIFY_*` below are copied once into the first admin's settings. The services are the box's: with Pocket Core they come from Core (Automatic), otherwise the `*_URL`/`*_API_KEY` below are copied once into Settings → Services as Direct connections, where they can be changed later.

| Variable | Default | What |
|---|---|---|
| `PORT`, `HOST` | `8080`, `0.0.0.0` | Listen address |
| `BASE_URL` | – | Public URL (links in notifications, the OIDC callback) |
| `DATA_DIR` | `./data` | Database, Core token, backups |
| `DB_PATH` | `$DATA_DIR/pocket-requests.db` | SQLite file |
| `MAX_BODY_BYTES` | `67108864` | Largest JSON request, e.g. an export being imported (64 MB) |
| `TZ` | – | Time zone for dates (needs `/usr/share/zoneinfo` in the container) |
| `AUTH_MODE` | `local` | `local` (accounts), `proxy` (trust `AUTH_PROXY_HEADER`/`AUTH_PROXY_EMAIL_HEADER`), `none` |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD`, `ADMIN_NAME` | – | Seed the first admin while the database is empty |
| `SESSION_TTL`, `SECURE_COOKIES` | `720h`, `false` | Set `SECURE_COOKIES=true` behind HTTPS |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_NAME`, `OIDC_AUTO_CREATE` | – | Sign in with an OpenID Connect provider (not needed with Pocket Core) |
| `RADARR_URL`, `RADARR_API_KEY` | – | Films |
| `SONARR_URL`, `SONARR_API_KEY` | – | Shows |
| `LIDARR_URL`, `LIDARR_API_KEY` | – | Albums |
| `PROWLARR_URL`, `PROWLARR_API_KEY` | – | Which indexers are down (Downloads page) |
| `JELLYFIN_URL`, `JELLYFIN_API_KEY` | – | "Can it really be watched?", Watch links, library scans |
| `JELLYSEERR_URL`, `JELLYSEERR_API_KEY` | – | Only to bring Jellyseerr's requests over (Settings → Backup & data) |
| `SLSKD_URL`, `SLSKD_API_KEY` | – | slskd: Soulseek downloads of requested albums (when Soularr fetches Lidarr's wanted albums), on the request and the Downloads page |
| `SOULARR_DIR` | – | Soularr's data folder (mount it, e.g. `/soularr`): albums on its `failed_imports.json` blacklist show as "needs a look", and admins can let Soulseek try again |
| `SEERR_HOSTS` | – | Host names (comma list) whose `/api/v1` is the Jellyseerr-compatible API — point Jellyseerr's old address here and its apps keep working; other pages there redirect to `BASE_URL` |
| `TMDB_API_KEY` | – | Trending / popular / best-rated shows on Discover (a v3 key or a v4 read token). Films and "new episodes this week" need no key |
| `*_PUBLIC_URL` | – | Where people open a service, when it differs from the address the app uses |
| `NOTIFY_URL`, `NOTIFY_FORMAT` | – | Apprise (`…/notify/key`), ntfy topic URL or JSON webhook; format `apprise`/`ntfy`/`json` |
| `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_BASE_URL` | – | "Suggest something" and questions without Pocket AI |
| `TRACK_EVERY` | `1m` | How often open requests are compared with Radarr/Sonarr/Lidarr |
| `CARE_EVERY` | `10m` | How often library care runs |
| `CORE_URL`, `CORE_REGISTRATION_KEY`, `CORE_APP_URL` | – | Pocket Core |
| `TRUST_PROXY`, `METRICS_ENABLED`, `LOG_LEVEL`, `LOG_FORMAT` | `true`, `true`, `info`, `text` | |
| `DEMO_DATA` | `false` | Seed a household's requests into the owner account (screenshots, trying it out) |

Commands: `pocket-requests backup [-o FILE]`, `pocket-requests reset-password [-email] [-password]`, `pocket-requests version`.

## Library care

Settings live on the Library care page (admins), per job: **Off**, **Watch only** (writes down what it would do, touches nothing — the default, so it can run next to existing scripts) or **Fix automatically**.

| Job | What it does |
|---|---|
| Search what was never searched | Monitored, no file, added in the last 3 days, no history at all → a search (at most 3) |
| Retry failed downloads | Monitored, missing, grabbed before, a failed download, nothing live in the queue → clear failed leftovers (blocklisted) and search again, backing off 30 min → 24 h |
| Finish blocked imports | "matched to series by ID" / "Movie title mismatch" → a manual import, only when every previewed file maps to the grabbed title (and episodes) with no rejections; anything else stuck is reported once |
| Make Jellyfin show new arrivals | A requested title is in the library but not in Jellyfin → a library scan (at most every 30 min, reported after 3) |

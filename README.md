# Hardened Containerized Invidious

[![CI](https://github.com/underhax/invidious-docker/actions/workflows/ci.yml/badge.svg)](https://github.com/underhax/invidious-docker/actions/workflows/ci.yml)
[![GitHub last commit](https://img.shields.io/github/last-commit/underhax/invidious-docker)](https://github.com/underhax/invidious-docker/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/underhax/invidious-docker)](https://github.com/underhax/invidious-docker/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/underhax/invidious-docker)](https://github.com/underhax/invidious-docker)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Hardened, zero-trust containerized deployment architecture for [Invidious](https://github.com/iv-org/invidious) and [Invidious Companion](https://github.com/iv-org/invidious-companion) running on minimal `FROM scratch` containers with PostgreSQL over an isolated UNIX domain socket, strict host Discretionary Access Control (DAC) boundaries, in-memory secret mediation with post-readiness environment scrubbing, and embedded zero-dependency healthcheck probes.

## Architecture & Security Model

* Invidious and Companion run in scratch containers without shells, package managers, or system utilities, containing only the upstream binaries, required shared libraries, CA certificates, and static Go helpers.
* The entrypoint reads database, proxy, and application secrets directly into memory and zeroes out the child process environment block once the service is ready, keeping credentials out of container metadata and process inspections.
* PostgreSQL runs with networking disabled and serves Invidious exclusively over a shared UNIX domain socket. Companion likewise uses a UNIX domain socket bridged locally to loopback inside the Invidious container.
* All containers run as a dedicated unprivileged host user and group with login disabled, isolating filesystem access from other host workloads.
* Services run with read-only root filesystems, all Linux capabilities dropped, privilege escalation disabled, isolated IPC namespaces, and explicit CPU, memory, and process limits.
* Persistent database directories, temporary mounts, and shared socket volumes disallow binary execution and setuid bits.
* No ports are published on the host; external traffic enters solely through an upstream reverse proxy network.
* A static Go probe binary verifies Invidious HTTP readiness and Companion socket availability directly without external utilities.

---

<details>
<summary><strong>Directory Structure</strong></summary>

Production directory layout on the host (`/home/invidious`):

```text
├── docker-compose.yaml                # Production deployment manifest
├── .env                               # Host UID/GID and image version overrides
├── companion.env                      # Optional Invidious Companion configuration
├── invidious.env                      # Optional Invidious server configuration
├── secrets/                           # Docker secrets directory
│   ├── db_name                        # PostgreSQL database name
│   ├── db_user                        # PostgreSQL database user
│   ├── db_password                    # PostgreSQL database password
│   ├── companion_key                  # Shared HMAC/auth key between Invidious and Companion
│   ├── inv_invidious_hmac_key         # Invidious HMAC signing key
│   ├── inv_invidious_domain           # Public Invidious domain (e.g. invidious.example.com)
│   ├── inv_invidious_external_port    # Public external port (e.g. 443)
│   ├── inv_invidious_admins           # Comma-separated list of admin usernames
│   ├── youtube_session_cookies        # Optional YouTube session cookies
│   ├── proxy_host                     # Optional SOCKS5 proxy host (Companion)
│   ├── proxy_port                     # Optional SOCKS5 proxy port (Companion)
│   ├── proxy_user                     # Optional SOCKS5 proxy username (Companion)
│   └── proxy_pass                     # Optional SOCKS5 proxy password (Companion)
└── postgres/                          # Persistent PostgreSQL data directory
```

</details>

---

<details>
<summary><strong>Deployment &amp; Setup</strong></summary>

### Prerequisites

* **Docker Engine & Compose:** Ensure Docker Engine and the Docker Compose plugin are installed on the host. Follow the official installation guide for [Ubuntu](https://docs.docker.com/engine/install/ubuntu/#install-using-the-repository).
* **Password Generator (`pwgen`):** Used below to generate high-entropy credentials (`sudo apt install -y pwgen` on Debian/Ubuntu).
* **External Gateway Network:** Required for connecting `invidious` and `invidious-companion` to an upstream reverse proxy (such as [Angie](https://github.com/webstudiobond/angie-docker-compose), which provisions `frontend_gateway` automatically). If deploying standalone, create the network manually:

  ```bash
  docker network create --subnet=172.20.0.0/16 frontend_gateway
  ```

### 1. Create a Dedicated System User

Choose a dedicated, unprivileged numeric ID unique to this service on your host and verify that it is not already in use by another user or group:

```bash
APP_UID=<CHOSEN_UID>

getent passwd ${APP_UID}
getent group ${APP_UID}
```

> **Important:** Both commands must return empty output. Each containerized service must have its own dedicated unprivileged user and group to enforce strict host DAC privilege separation between different stacks.

Create an unprivileged system account with a matching user-group (`-U`) and disabled login shell:

```bash
sudo useradd -M -d /home/invidious -s /usr/sbin/nologin -u ${APP_UID} -U invidious
```

### 2. Create Directory Layout & Assign Ownership

Create the base directory, persistent PostgreSQL storage, and secrets directory from `root` with strict DAC permissions:

```bash
sudo mkdir -p /home/invidious
sudo chown root:invidious /home/invidious
sudo chmod 0750 /home/invidious

sudo mkdir -p /home/invidious/{postgres,secrets}

sudo chown root:invidious /home/invidious/secrets
sudo chmod 0750 /home/invidious/secrets

sudo chown invidious:invidious /home/invidious/postgres
sudo chmod 0700 /home/invidious/postgres
```

### 3. Deploy Project Files

Download the production manifests directly into `/home/invidious`:

```bash
REPO="https://raw.githubusercontent.com/underhax/invidious-docker/main"

sudo curl -fsSL ${REPO}/docker-compose.yaml -o /home/invidious/docker-compose.yaml
sudo curl -fsSL ${REPO}/examples/.env.example -o /home/invidious/.env
sudo curl -fsSL ${REPO}/examples/companion.env.example -o /home/invidious/companion.env
sudo curl -fsSL ${REPO}/examples/invidious.env.example -o /home/invidious/invidious.env

sudo chown root:root /home/invidious/{docker-compose.yaml,.env,companion.env,invidious.env}
sudo chmod 0600 /home/invidious/{docker-compose.yaml,.env,companion.env,invidious.env}
```

### 4. Configure Environment Settings

Set `APP_UID` and `APP_GID` in `/home/invidious/.env` to match the dedicated user created in step 1, then review and customize `.env`, `companion.env`, and `invidious.env`:

```bash
sed -i "s/^APP_UID=.*/APP_UID=${APP_UID}/" /home/invidious/.env
sed -i "s/^APP_GID=.*/APP_GID=${APP_UID}/" /home/invidious/.env
```

```bash
sudo nano /home/invidious/.env
sudo nano /home/invidious/companion.env
sudo nano /home/invidious/invidious.env
```

### 5. Configure Docker Secrets

Initialize the required secret files with strict DAC permissions:

```bash
sudo touch /home/invidious/secrets/{db_name,db_user,db_password,companion_key,inv_invidious_hmac_key,inv_invidious_domain,inv_invidious_external_port,inv_invidious_admins}
sudo chown root:invidious /home/invidious/secrets/*
sudo chmod 0440 /home/invidious/secrets/*
```

Generate the database credentials, Companion shared key, and Invidious HMAC key:

```bash
printf "db_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/invidious/secrets/db_name > /dev/null
printf "u_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/invidious/secrets/db_user > /dev/null
pwgen -s 64 1 | tr -d '\n' | sudo tee /home/invidious/secrets/db_password > /dev/null
pwgen -s 16 1 | tr -d '\n' | sudo tee /home/invidious/secrets/companion_key > /dev/null
pwgen -s 64 1 | tr -d '\n' | sudo tee /home/invidious/secrets/inv_invidious_hmac_key > /dev/null
```

Populate the instance domain, external port (e.g. `443`), and admin username(s):

```bash
sudo nano /home/invidious/secrets/inv_invidious_domain
sudo nano /home/invidious/secrets/inv_invidious_external_port
sudo nano /home/invidious/secrets/inv_invidious_admins
```

> **Note:** Any secret file mounted under `/run/secrets` with the `inv_` prefix is automatically mapped by `init` to the corresponding uppercase environment variable (for example, `inv_invidious_hmac_key` becomes `INVIDIOUS_HMAC_KEY`, and `inv_server_secret_key` becomes `SERVER_SECRET_KEY`).

#### Optional Secrets (YouTube Cookies & SOCKS5 Proxy)

`docker-compose.yaml` includes commented secret definitions for optional YouTube session cookies and Companion SOCKS5 proxy authentication:

1. **YouTube Session Cookies (`youtube_session_cookies`):**
   Create `/home/invidious/secrets/youtube_session_cookies` (`root:invidious`, `0440`) and uncomment the `youtube_session_cookies` entries under `companion.secrets`, `invidious.secrets`, and top-level `secrets` in `docker-compose.yaml`.

2. **SOCKS5 Proxy (`proxy_host`, `proxy_port`, `proxy_user`, `proxy_pass`):**
   Create the required `proxy_host` and `proxy_port` secret files (plus `proxy_user` and `proxy_pass` if proxy authentication is enabled) with `root:invidious` (`0440`), and uncomment the `proxy_*` entries under `companion.secrets` and top-level `secrets` in `docker-compose.yaml`. `init` builds the `PROXY` (`socks5://...`) environment variable automatically in memory.

### 6. Host Permissions &amp; Privilege Separation

Verify final ownership and permissions across the stack directory:

```bash
sudo chown root:invidious /home/invidious
sudo chmod 0750 /home/invidious

sudo chown root:root /home/invidious/{docker-compose.yaml,.env,companion.env,invidious.env}
sudo chmod 0600 /home/invidious/{docker-compose.yaml,.env,companion.env,invidious.env}

sudo chown -R root:invidious /home/invidious/secrets
sudo find /home/invidious/secrets -type d -exec chmod 0750 {} +
sudo find /home/invidious/secrets -type f -exec chmod 0440 {} +

sudo chown -R invidious:invidious /home/invidious/postgres
sudo find /home/invidious/postgres -type d -exec chmod 0700 {} +
sudo find /home/invidious/postgres -type f -exec chmod 0600 {} +
```

### 7. Start the Stack

Pull the latest images and start the stack in detached mode:

```bash
sudo docker compose -f /home/invidious/docker-compose.yaml pull
sudo docker compose -f /home/invidious/docker-compose.yaml up -d
```

</details>

---

<details>
<summary><strong>Operation &amp; Maintenance</strong></summary>

### Verifying Service Health

Check container status and resource consumption:

```bash
sudo docker compose -f /home/invidious/docker-compose.yaml ps
sudo docker stats --no-stream invidious invidious-companion invidious-db
```

### Inspecting Runtime Logs

Follow service logs in real time:

```bash
sudo docker compose -f /home/invidious/docker-compose.yaml logs -f
```

### Updating the Images

Update to the latest releases and recreate the containers:

```bash
sudo docker compose -f /home/invidious/docker-compose.yaml pull
sudo docker compose -f /home/invidious/docker-compose.yaml up -d
```

### Tearing Down the Stack

Stop and remove the containers:

```bash
sudo docker compose -f /home/invidious/docker-compose.yaml down
```

</details>

---

<details>
<summary><strong>Development</strong></summary>

For local binary compilation, unit testing, static analysis, and CI/CD pipeline details, see **[DEVELOPMENT.md](DEVELOPMENT.md)**.

</details>

---

<details>
<summary><strong>License &amp; Attribution</strong></summary>

## License & Attribution

This repository and deployment architecture are licensed under the [MIT License](LICENSE). This project is an independent containerized deployment architecture and is not affiliated with, endorsed, or sponsored by the Invidious organization.

* **Invidious:** Upstream software developed by the [Invidious project](https://github.com/iv-org/invidious).
* **Invidious Companion:** Upstream companion service developed by the [Invidious project](https://github.com/iv-org/invidious-companion).

</details>

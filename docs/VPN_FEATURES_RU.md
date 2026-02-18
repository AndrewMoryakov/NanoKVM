# VPN-функции NanoKVM: Tailscale и NetBird

## Обзор

NanoKVM поддерживает два VPN-решения для удалённого доступа к устройству:

- **Tailscale** — облачный mesh VPN (встроен в прошивку)
- **NetBird** — mesh VPN с возможностью self-hosted сервера

Из-за ограниченной памяти NanoKVM Cube (161 МБ RAM) одновременно может работать только один VPN-сервис. Система автозапуска обеспечивает запуск выбранного VPN при загрузке, а второй остаётся выключенным.

---

## 1. Переключатель автозапуска VPN

### Как работает

При загрузке устройства init-скрипт (`S95nanokvm`) читает файл `/etc/kvm/vpn` и запускает только выбранный VPN:

- `tailscale` (по умолчанию) — запускается Tailscale, NetBird не запускается
- `netbird` — запускается NetBird, Tailscale не запускается

### Управление через Web UI

В настройках Tailscale и NetBird рядом с заголовком расположен переключатель **Autostart**:

1. Откройте `Settings` в меню KVM
2. Перейдите на вкладку **Tailscale** или **NetBird**
3. Переключатель рядом с названием показывает, какой VPN является автозапускаемым
4. Нажмите на переключатель для смены — система:
   - Остановит текущий VPN
   - Запустит выбранный VPN
   - Сохранит выбор в `/etc/kvm/vpn`
   - Выбор сохраняется при перезагрузке

### Управление через API

```bash
# Получить текущий выбор
curl http://<KVM_IP>/api/extensions/vpn/preference \
  -H "Authorization: Bearer <TOKEN>"

# Ответ: {"code":0,"data":{"vpn":"tailscale"}}

# Переключить на NetBird
curl -X POST http://<KVM_IP>/api/extensions/vpn/preference \
  -H "Authorization: Bearer <TOKEN>" \
  -d '{"vpn":"netbird"}'
```

### Управление через SSH

```bash
# Посмотреть текущий выбор
cat /etc/kvm/vpn

# Переключить вручную
echo "netbird" > /etc/kvm/vpn
/etc/init.d/S95nanokvm restart
```

---

## 2. Tailscale

Tailscale — облачный VPN с простой настройкой. Встроен в прошивку NanoKVM.

### Подключение

1. `Settings` → `Tailscale` → `Login`
2. Откроется ссылка для авторизации через Tailscale
3. После авторизации устройство получит IP в сети `100.x.x.x`

### Управление

- **Restart** — перезапустить демон Tailscale
- **Stop** — остановить Tailscale
- **Memory** — настроить лимит памяти Go-рантайма (GOMEMLIMIT)
- **Swap** — управление swap-файлом
- **Uninstall** — удалить Tailscale

### Диагностика

```bash
ssh root@<KVM_IP>
tailscale status
tailscale status --json
/etc/init.d/S98tailscaled status
```

---

## 3. NetBird

NetBird — mesh VPN с поддержкой self-hosted серверов. Бинарник NetBird поставляется в `/kvmapp/system/netbird/netbird`.

### Режимы подключения

#### Official Server (облачный)

1. `Settings` → `NetBird` → `Connect` → выбрать **Official Server**
2. Нажать **Login with NetBird**
3. Откроется ссылка авторизации NetBird
4. После входа нажать **I've logged in**

#### Custom Server (self-hosted)

Для подключения к собственному серверу NetBird:

1. `Settings` → `NetBird` → `Connect` → выбрать **Custom Server**
2. Заполнить:
   - **Setup Key** — ключ установки (получить у администратора сервера)
   - **Management URL** — URL сервера управления (например, `https://185.177.219.147:33073`)
   - **Admin URL** (опционально) — URL панели администратора
3. Нажать **Connect**

### Переподключение / Смена сервера

Если устройство уже подключено, но нужно сменить сервер:

1. `Settings` → `NetBird` → нажать **Reconfigure** (доступна когда VPN остановлен)
2. Откроется форма подключения с текущим Management URL
3. Ввести новые данные и подключиться

### Управление

- **Restart** — перезапустить демон NetBird
- **Stop** — остановить NetBird
- **Disconnect** — отключиться от сети (с подтверждением)
- **Refresh** — обновить статус

### Информация об устройстве

Когда устройство подключено, отображается:

- **State** — состояние (running / stopped)
- **Device Name** — имя устройства в сети
- **Device IP** — VPN IP-адрес (обычно `100.x.x.x`)
- **Management URL** — URL сервера управления
- **Version** — версия клиента NetBird

### Диагностика

```bash
ssh root@<KVM_IP>

# Статус
netbird status --daemon-addr unix:///var/run/netbird.sock
netbird status --json --daemon-addr unix:///var/run/netbird.sock

# Логи
cat /tmp/netbird/client.log

# Init-скрипт
/etc/init.d/S99netbird status

# Проверить сокет демона
ls -la /var/run/netbird.sock
```

### Частые проблемы NetBird

| Проблема | Причина | Решение |
|----------|---------|---------|
| Спиннер крутится, потом ошибка | Демон не запущен / сокет не создан | `netbird service start`, проверить `/tmp/netbird/client.log` |
| TLS handshake error / "not yet valid" | Часы устройства сбились (нет RTC батарейки) | Синхронизировать часы: `ntpd -n -q -p time.google.com` или `date -s 'YYYY-MM-DD HH:MM:SS'` |
| Пинг через туннель 100% packet loss | iptables bug на RISC-V: пустая цепочка `NETBIRD-ACL-INPUT` | `iptables -I NETBIRD-ACL-INPUT -s 100.96.0.0/16 -j ACCEPT` (в прошивке фикс в S99netbird) |
| Старый сервер в конфиге | Предыдущий `default.json` не очищен | Удалить `/var/lib/netbird/default.json`, перезапустить |
| Устройство зависает после старта NetBird | OOM (158MB RAM, ~50MB под сервисы) | Включить swap в S95nanokvm (по умолчанию 128MB) |
| exit status 1 | Разные причины | Проверить `/tmp/netbird/client.log` |

### Известные баги платформы

#### iptables ACL не заполняется на RISC-V

NetBird firewall manager на RISC-V/Linux не заполняет iptables цепочку `NETBIRD-ACL-INPUT` ACL-правилами, даже при подключённом management-сервере с политикой "allow all". WireGuard туннель устанавливается, хендшейк проходит, пакеты приходят на интерфейс `wt0`, но все дропаются пустой ACL-цепочкой.

**Workaround в прошивке:** Init-скрипт `S99netbird` после запуска демона ждёт появления iptables-цепочки и вставляет правило `ACCEPT` для всей подсети `100.96.0.0/16`.

Подробнее: `NETBIRD_IPTABLES_ISSUE.md`

#### Нет RTC батарейки

NanoKVM Cube не имеет RTC батарейки. При каждом отключении питания часы сбрасываются. Это ломает TLS-соединения (Let's Encrypt сертификат выглядит как "not yet valid").

**Workaround в прошивке:** Init-скрипт `S95nanokvm` синхронизирует часы через NTP (time.google.com / pool.ntp.org / time.cloudflare.com) перед запуском VPN-сервисов.

---

## 4. Развёртывание Self-Hosted NetBird сервера

NanoKVM поддерживает подключение к собственному серверу NetBird. Для развёртывания предусмотрен скрипт автоматической установки.

### Быстрый старт

```bash
# На вашем VPS-сервере (Ubuntu/Debian, публичный IPv4):
chmod +x scripts/deploy_netbird_server.sh
./scripts/deploy_netbird_server.sh
```

Скрипт спросит:
- **Домен** — должен указывать на IP сервера (например, `mynetbird.duckdns.org`)
- **Email** — для уведомлений Let's Encrypt

И автоматически:
- Установит Docker (если не установлен)
- Сгенерирует криптографические ключи
- Создаст конфигурации (config.yaml, dashboard.env, docker-compose.yml)
- Остановит конфликтующие сервисы (nginx, apache)
- Откроет порты в firewall
- Запустит контейнеры: Traefik + Dashboard + netbird-server
- Проверит работоспособность

### После развёртывания

1. Откройте `https://ваш-домен` — зарегистрируйте admin-аккаунт
2. Создайте Setup Key: Dashboard → Setup Keys → Create
3. Подключите устройства:

**Windows/Mac/Linux:**
```bash
netbird up --management-url https://ваш-домен --setup-key <КЛЮЧ>
```

**NanoKVM Cube:**
Settings → NetBird → Install → Custom Server → ввести Management URL и Setup Key

### Требования к серверу

| Ресурс | Минимум |
|--------|---------|
| CPU | 1 vCPU |
| RAM | 1 GB |
| Диск | 10 GB |
| Порты | 80, 443 (TCP), 3478 (UDP) |

Подробная документация: `docs/SERVER_STATUS.md`, `NETBIRD_SELFHOSTED_DEPLOYMENT.md`

---

## 5. Скрипты

| Скрипт | Описание |
|--------|----------|
| `scripts/deploy_netbird_server.sh` | Развёртывание self-hosted NetBird сервера на VPS |
| `scripts/build_cube_overlay.sh` | Сборка прошивки NanoKVM Cube (backend + frontend) |
| `scripts/deploy_cube.sh` | Деплой прошивки на NanoKVM Cube через SSH |

---

## 6. API-эндпоинты

### VPN Preference

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/api/extensions/vpn/preference` | Получить текущий автозапуск |
| POST | `/api/extensions/vpn/preference` | Установить автозапуск (`{"vpn":"tailscale"}` или `{"vpn":"netbird"}`) |

### Tailscale

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/api/extensions/tailscale/status` | Статус Tailscale |
| POST | `/api/extensions/tailscale/login` | Получить URL авторизации |
| POST | `/api/extensions/tailscale/up` | Подключиться |
| POST | `/api/extensions/tailscale/down` | Отключиться |
| POST | `/api/extensions/tailscale/start` | Запустить демон |
| POST | `/api/extensions/tailscale/stop` | Остановить демон |
| POST | `/api/extensions/tailscale/restart` | Перезапустить демон |
| POST | `/api/extensions/tailscale/install` | Установить |
| POST | `/api/extensions/tailscale/uninstall` | Удалить |
| POST | `/api/extensions/tailscale/logout` | Выйти |

### NetBird

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/api/extensions/netbird/status` | Статус NetBird |
| POST | `/api/extensions/netbird/login` | Логин (Official Server) |
| POST | `/api/extensions/netbird/up` | Подключиться (Custom Server) с `setupKey`, `managementUrl`, `adminUrl` |
| POST | `/api/extensions/netbird/down` | Отключиться |
| POST | `/api/extensions/netbird/start` | Запустить демон |
| POST | `/api/extensions/netbird/stop` | Остановить демон |
| POST | `/api/extensions/netbird/restart` | Перезапустить демон |
| POST | `/api/extensions/netbird/install` | Установить |
| POST | `/api/extensions/netbird/uninstall` | Удалить |

---

## 7. Файлы и пути

### На устройстве NanoKVM

| Путь | Описание |
|------|----------|
| `/etc/kvm/vpn` | Файл выбора VPN (`tailscale` или `netbird`) |
| `/etc/init.d/S95nanokvm` | Главный init-скрипт |
| `/etc/init.d/S98tailscaled` | Init-скрипт Tailscale |
| `/etc/init.d/S99netbird` | Init-скрипт NetBird |
| `/usr/bin/netbird` | Бинарник NetBird |
| `/usr/sbin/tailscaled` | Демон Tailscale |
| `/usr/bin/tailscale` | CLI Tailscale |
| `/var/lib/netbird/default.json` | Конфиг клиента NetBird |
| `/var/run/netbird.sock` | Unix-сокет демона NetBird |
| `/tmp/netbird/client.log` | Логи NetBird |
| `/kvmapp/system/netbird/netbird` | Бинарник NetBird в пакете |
| `/kvmapp/system/init.d/S99netbird` | Init-скрипт NetBird в пакете |

### В репозитории

| Путь | Описание |
|------|----------|
| `server/service/extensions/vpn/service.go` | Сервис VPN-переключателя |
| `server/service/extensions/netbird/` | Сервис NetBird (cli.go, service.go) |
| `server/service/extensions/tailscale/` | Сервис Tailscale |
| `server/router/extensions.go` | Роутер расширений |
| `server/proto/vpn.go` | Proto-типы VPN |
| `server/proto/netbird.go` | Proto-типы NetBird |
| `web/src/api/extensions/vpn.ts` | API-клиент VPN |
| `web/src/api/extensions/netbird.ts` | API-клиент NetBird |
| `web/src/pages/desktop/menu/settings/netbird/` | UI NetBird |
| `web/src/pages/desktop/menu/settings/tailscale/` | UI Tailscale |
| `kvmapp/system/init.d/S95nanokvm` | Init-скрипт (исходник) |
| `kvmapp/system/init.d/S99netbird` | Init-скрипт NetBird (исходник) |

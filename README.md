# xray-waybar-ctl

CLI на Go, который управляет `xray-core` и отдаёт статус кастомному модулю Waybar.
Без GUI, без Electron — `xray-waybar-ctl status` печатает одну строку JSON, всё остальное
прячется за `connect` / `disconnect` / `use-next`.

## Возможности

- Скачивает подписку (base64 → список `vless://` / `vmess://` / `trojan://`).
- Парсит VLESS+REALITY, TLS, WS, gRPC.
- Параллельно URL-тестит серверы и выбирает живой с минимальной латентностью
  (или первый из заданного `priority`).
- Запускает xray-core как detached процесс с PID-файлом, ждёт открытия SOCKS5 порта.
- Печатает JSON для Waybar (`connected` / `disconnected` / `loading` / `error`).
- Команды переключения: `use <name>`, `use-next`, `use-prev`, `reconnect`, `toggle`.

## Быстрая установка в Waybar

Нужны **Linux с Waybar**, `xray`, `python3`, `curl` и `tar`. На x86-64
и ARM64 установщик скачивает опубликованный бинарник и проверяет SHA-256;
Go нужен только для сборки из исходников или неподдерживаемой архитектуры
(`go.mod` сейчас требует Go 1.26.2). Для меню по правому клику подойдёт **любой один**
из `walker`, `wofi`, `rofi`; остальное работает и без меню. `tun2socks`
нужен только для отдельного system-wide режима.

```sh
curl -fsSL https://raw.githubusercontent.com/hollowgxd/xray-waybar-ctl/v0.1.2/install.sh | bash
```

Команда использует закреплённый установщик, скачивает последний стабильный релиз и его исходники (или собирает CLI из исходников), кладёт его в
`~/.local/bin/xray-waybar-ctl` и **сама добавляет** `custom/xray` в активный
`~/.config/waybar/config` (или `config.jsonc`) и стиль в `style.css`.
Изменённые файлы Waybar получают бэкап рядом с собой. Повторный запуск
обновляет бинарник, но не плодит модули и не затирает существующий
`~/.config/xray-waybar/app.yaml`. Никакого `sudo` и изменения сетевых
маршрутов при установке нет.

Если в панели уже стоит HAPP и его нужно заменить **только в списке модулей**:

```sh
curl -fsSL https://raw.githubusercontent.com/hollowgxd/xray-waybar-ctl/v0.1.2/install.sh | bash -s -- --replace-happ
```

Скрипты/стиль HAPP сохраняются; они просто перестают использоваться в панели.
Чтобы установить из клона: `bash install.sh [--replace-happ]`.
`--no-reload` оставит работающий Waybar без перезапуска. Исходный конфиг можно
восстановить из `*.xray-waybar-backup-*` рядом с ним.

**Последний шаг — URL подписки.** На первой установке создаётся приватный
`~/.config/xray-waybar/app.yaml` с пустым `subscription_url` и очевидным
статусом ошибки в Waybar. Вставь свой URL в этот файл, затем:

```sh
~/.local/bin/xray-waybar-ctl update
~/.local/bin/xray-waybar-ctl connect
```

URL и токен не передаются в командной строке установщика и не попадают в
README/логи. По умолчанию запускается локальный SOCKS5 `127.0.0.1:1080`:
**другие приложения не начнут пользоваться VPN автоматически**. Для всего
трафика см. [System-wide режим](#system-wide-режим-весь-трафик-через-vpn).

### Что делает модуль

| Действие | Результат |
| --- | --- |
| Левый клик | Подключить / отключить |
| Правый клик | Меню серверов и профилей (`walker`, `wofi` или `rofi`) |
| Средний клик | Переподключить |
| Колесо вверх / вниз | Следующий / предыдущий сервер |
| Наведение | Сервер, endpoint, задержка, режим и время работы |

Цвета: фиолетовый — отключено, зелёный — подключено, жёлтый — подключение,
красный — ошибка или неактивный TUN. Блок CSS использует собственные цвета и
не требует переменных чужой темы. Статус CLI отдаёт JSON прямо в Waybar,
без HAPP и без GUI-клиента. Для пиктограмм нужен Nerd Font.

### Ручная установка / конфигурация

`make install` собирает CLI и пример конфига, **но не меняет Waybar**.
Если нужен полностью ручной контроль, используй его и пример ниже. Для
нестандартной конфигурации скопируй шаблон из
`~/.local/share/xray-waybar/app.yaml.example` и настрой поля. Минимально
требуется `subscription_url`; `routing_profile: proxy-all` работает без
geoip/geosite. Для `smart`/`whitelist` запусти `xray-waybar-ctl update-geo`.

Для `systemd`-мониторинга/автореконнекта есть отдельный
`make install-monitor`. Он не включается установщиком автоматически и
не нужен для обычного управления из Waybar.

## Использование

```sh
xray-waybar-ctl update      # подтянуть подписку в локальный кэш
xray-waybar-ctl list        # посмотреть, что в кэше
xray-waybar-ctl test        # таблица латентностей
xray-waybar-ctl connect     # тест + автоматический выбор + запуск
xray-waybar-ctl use DE-2    # подключиться к конкретному серверу
xray-waybar-ctl status      # JSON для Waybar
```

## System-wide режим (весь трафик через VPN)

По умолчанию xray-waybar-ctl поднимает только локальный SOCKS5 на `127.0.0.1:1080`,
и приложения должны сами через него ходить. Чтобы весь трафик системы автоматически
шёл через VPN — включи system-wide режим через TUN.

### Установка

```sh
yay -S tun2socks            # gVisor-based, лежит в AUR
sudo make install-system    # systemd unit + polkit rule + pacman hook
```

`install-system` ставит:
- `xray-waybar-tun.service` (systemd unit для tun2socks)
- polkit-правило, разрешающее членам группы `wheel` поднимать/гасить именно этот unit без пароля
- pacman-хук, который реприменяет `cap_net_admin` на `/usr/bin/xray` после каждого `pacman -Syu`

Пользователь должен быть в группе `wheel` — иначе polkit откажет и `connect`
не сможет поднять TUN:

```sh
groups | grep -q wheel || sudo usermod -aG wheel "$USER"   # перелогиниться
```

В `~/.config/xray-waybar/app.yaml`:

```yaml
system_wide: true
```

После этого `xray-waybar-ctl connect` запускает xray и поднимает `xray-waybar-tun.service`
— весь IP-трафик уходит в `tun0`, кроме приватных подсетей:

- `10.0.0.0/8`
- `172.16.0.0/12`
- `192.168.0.0/16`

Они маршрутизируются мимо туннеля, как и был запрос. Список захардкожен в
`configs/systemd/xray-waybar-tun.service` — если нужны ещё bypass-CIDR, добавляй
`ExecStartPost`/`ExecStopPost` строки там и `sudo systemctl daemon-reload`.

`disconnect` останавливает unit перед тем как глушить xray (иначе пакеты ушли бы
в мёртвый SOCKS5). Если хочешь убрать system-wide вообще — `sudo make uninstall-system`.

Проверка:
```sh
ip route                                # должен быть `default dev tun0 metric 1`
curl https://ifconfig.me                # IP сервера, не твой
curl http://10.0.0.1                    # идёт напрямую (если этот хост у тебя есть)
```

## Мониторинг и автореконнект

```sh
make install-monitor
```

Ставит и включает три user-юнита:

- `xray-waybar-ping.timer` — раз в 30s гоняет ping-батч, чтобы тултип и
  меню walker показывали актуальные латентности.
- `xray-waybar-watchdog.service` — long-lived демон с внутренним 10s
  loop. Ловит две вещи: (1) xray умер за tun2socks → автореконнект,
  (2) система проснулась после suspend → tear down + reconnect через
  D-Bus signal `PrepareForSleep` от logind.
- `xray-waybar-geo.timer` — раз в сутки обновляет `geoip.dat` /
  `geosite.dat` из `roscomvpn-geo*` GitHub Releases.

Снять: `make uninstall-monitor`.

Логи watchdog'а:

```sh
systemctl --user status xray-waybar-watchdog.service
journalctl --user -u xray-waybar-watchdog.service -f
```

## Меню переключения

Команда `xray-waybar-ctl menu` поднимает dmenu-совместимый лаунчер со списком
серверов; первой строкой — текущий профиль. Выбор сервера → `use`,
выбор `Profile: …` → второй экран меню с вариантами `routing_profile`
(proxy-all / direct / smart / whitelist / custom URL). Запись идёт в
`state.json` и перебивает значение из `app.yaml` до
`xray-waybar-ctl profile reset`.

Биндить на хоткей оконного менеджера или на `on-click` модуля Waybar.
`walker`, `wofi` или `rofi` должен быть в `$PATH` — иначе `menu`
вернёт ошибку, остальной CLI работает.

## Ручное подключение Waybar

Если не используешь `install.sh`, добавь `custom/xray` в нужный массив
`modules-left`, `modules-center` или `modules-right`, а в корневой объект
конфига Waybar — модуль:

```jsonc
"custom/xray": {
  "exec": "~/.local/bin/xray-waybar-ctl status",
  "return-type": "json",
  "interval": 5,
  "format": "{}",
  "on-click": "~/.local/bin/xray-waybar-ctl toggle",
  "on-click-right": "~/.local/bin/xray-waybar-ctl menu",
  "on-click-middle": "~/.local/bin/xray-waybar-ctl reconnect",
  "on-scroll-up": "~/.local/bin/xray-waybar-ctl use-next",
  "on-scroll-down": "~/.local/bin/xray-waybar-ctl use-prev"
}
```

Стили: см. `scripts/waybar-integrate.py` или запусти установщик для
автоматической интеграции. После ручных правок `pkill -SIGUSR2 waybar`.

## Разработка

```sh
make test       # юнит-тесты
make vet
make build      # выдаёт ./xray-waybar-ctl
```

## Лицензия

MIT.

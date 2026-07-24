# xray-waybar-ctl

CLI на Go, который управляет `xray-core` **или** `mihomo` и отдаёт статус кастомному модулю Waybar.
Без GUI, без Electron — `xray-waybar-ctl status` печатает одну строку JSON, всё остальное
прячется за `connect` / `disconnect` / `use-next`.

## Возможности

- Выбираемое ядро через `core: xray | mihomo`; `xray` остаётся дефолтом для обратной совместимости.
- Скачивает base64/URI, Xray JSON array и нативные Mihomo YAML-подписки.
- Парсит VLESS+REALITY, TLS, WS, gRPC.
- Xray параллельно тестирует отдельными временными процессами; Mihomo тестирует и
  переключает группы через localhost REST API без рестарта ядра.
- В Mihomo режиме сохраняет полную YAML-политику (rules, DNS, providers, URL-test,
  fallback) и достраивает безопасный `VPN → AUTO`, если подписка содержит только прокси.
- Запускает выбранное ядро как detached процесс с PID-файлом и проверкой готовности.
- Печатает JSON для Waybar (`connected` / `disconnected` / `loading` / `error`).
- Команды переключения: `use <name>`, `use-next`, `use-prev`, `reconnect`, `toggle`.

## Установка

### Самый простой способ: Mihomo canary

На Linux x86-64/ARM64 запусти мастер:

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/hollowgxd/xray-waybar-ctl/v0.1.0-canary.1/install.sh)
```

Он сам:

- скачает проверенные готовые бинарники `xray-waybar-ctl` и официального Mihomo;
- спросит URL подписки и сохранит конфиг с правами `0600`;
- по умолчанию включит native Mihomo TUN для всей системы;
- установит user-systemd watchdog и периодическую проверку соединения;
- сразу проверит подписку и попробует подключиться;
- положит готовые сниппеты для Waybar в `~/.local/share/xray-waybar/`.

Go, клонирование репозитория и ручное редактирование YAML не нужны. Установщик
не перезаписывает уже существующий `~/.config/xray-waybar/app.yaml`, поэтому той
же командой можно безопасно обновить бинарники.

Полезные варианты:

```sh
# установить без первого подключения
bash <(curl -fsSL https://raw.githubusercontent.com/hollowgxd/xray-waybar-ctl/v0.1.0-canary.1/install.sh) --no-connect

# локальный HTTP/SOCKS вместо системного VPN/TUN
bash <(curl -fsSL https://raw.githubusercontent.com/hollowgxd/xray-waybar-ctl/v0.1.0-canary.1/install.sh) --no-tun

# удалить программу, сохранив конфиг и кэш
bash <(curl -fsSL https://raw.githubusercontent.com/hollowgxd/xray-waybar-ctl/v0.1.0-canary.1/install.sh) --uninstall
```

Для показа значка всё ещё нужно добавить `custom/vpn` в один из массивов
`modules-left` / `modules-center` / `modules-right` своего конфига Waybar.
Установщик печатает точные пути к готовому модулю и CSS, но намеренно не
переписывает пользовательский JSONC автоматически.

### Сборка из исходников

Нужно поставить:

- одно ядро: `xray-core` или `mihomo`
- `waybar` — куда выводится статус
- `walker` — dmenu-лаунчер для встроенного меню переключения серверов
  (`xray-waybar-ctl menu`)
- Go ≥ 1.21 — для сборки
- `tun2socks` — только для `core: xray` + `system_wide: true`; у Mihomo TUN нативный

На Arch Xray-вариант можно собрать после `yay -S xray waybar walker-bin tun2socks go`.
Для Mihomo установи официальный бинарник `mihomo` вместо `xray`/`tun2socks`.
На Debian/Ubuntu/Fedora — штатный пакетный менеджер; `walker` и `tun2socks`
если в репах нет, берутся из релизов:
<https://github.com/abenz1267/walker>,
<https://github.com/xjasonlyu/tun2socks/releases>.

```sh
make install
```

`make install` кладёт бинарь в `~/.local/bin/xray-waybar-ctl` и пример конфига
в `~/.local/share/xray-waybar/app.yaml.example`. Убедись, что `~/.local/bin`
есть в `$PATH`.

### Что предполагает проект

- **systemd как init** — мониторинг и TUN-сайдкар поднимаются через
  systemd-юниты (user-session и system). Без systemd `make install-monitor`
  и `make install-system` не сработают; основной `connect/disconnect`
  будет работать, но без автореконнекта и без TUN.
- **logind на system D-Bus** — watchdog ловит `PrepareForSleep` от
  `org.freedesktop.login1` для tear-down/resume. На системах без logind
  (runit/OpenRC) suspend/resume в watchdog'е молча отключится, тики по
  таймеру остаются.
- **polkit + группа `wheel`** — нужны только для `system_wide: true`,
  чтобы юзер мог поднимать `xray-waybar-tun.service` без пароля
  (`configs/polkit/50-xray-waybar.rules`). На дистрибутивах, где
  административная группа называется иначе (`sudo` в Debian),
  отредактируй правило либо добавь себя в `wheel`:
  `sudo groupadd -f wheel && sudo usermod -aG wheel "$USER"`.
- **nerd-font на панели** — иконки в `internal/waybar` это
  нерд-глифы. Если у тебя нет нерд-шрифта, в Waybar будут квадратики
  — поставь любой nerd-font и пропиши его в `font-family` стиля
  модуля.
- **pacman-хук** (`configs/pacman/xray-waybar.hook`) реприменяет
  `cap_net_admin` на `/usr/bin/xray` после `pacman -Syu`. На не-Arch
  дистрибутивах хук не сработает — после обновления xray руками:
  `sudo setcap cap_net_admin+ep /usr/bin/xray`.

## Конфигурация

Скопируй пример и подставь свой URL подписки:

```sh
mkdir -p ~/.config/xray-waybar
cp ~/.local/share/xray-waybar/app.yaml.example ~/.config/xray-waybar/app.yaml
$EDITOR ~/.config/xray-waybar/app.yaml
```

Минимально нужны `subscription_url` и выбор `core` (по умолчанию `xray`). Все остальные поля имеют разумные дефолты —
см. `configs/app.yaml.example`.

### Mihomo backend

```yaml
core: mihomo
subscription_url: "https://panel.example/sub/token"
xray_port: 1080             # mixed HTTP/SOCKS port в режиме Mihomo
mihomo_controller: "127.0.0.1:9090"
mihomo_group: ""            # авто; лучше задать имя, если select-групп несколько
system_wide: true            # native Mihomo TUN
```

Запрос подписки отправляется с `User-Agent: Clash-Meta/xray-waybar-ctl`, поэтому
панель может вернуть свой полноценный Mihomo/Clash.Meta шаблон. Он используется
как основной источник routing/DNS/groups/providers, но controller, secret и bind
принудительно остаются локальными.

Если local path всё же отдаёт массив полных Xray JSON-конфигов, CLI не передаёт
его в Mihomo «как есть»: он извлекает VLESS/VMess/Trojan outbounds (TCP/WS/gRPC,
а для VLESS также XHTTP; TLS/REALITY) и строит минимальный Mihomo YAML с
`VPN` и `AUTO/url-test`. Это fallback;
для production предпочтительнее отдельный нативный Mihomo-шаблон панели — только
так сохраняются все её rules, DNS, proxy-providers, sniffer и дополнительные протоколы.

Для Xray: если `routing_profile` не `proxy-all` / `direct` — один раз нужно скачать
geoip/geosite, иначе xray откажется стартовать:

```sh
xray-waybar-ctl update-geo
```

Дальше это делает таймер `xray-waybar-geo.timer` из `make install-monitor`.

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

По умолчанию xray-waybar-ctl поднимает только локальный SOCKS5/mixed порт на `127.0.0.1:1080`,
и приложения должны сами через него ходить. Чтобы весь трафик системы автоматически
шёл через VPN — включи system-wide режим через TUN.

### Xray: внешний tun2socks

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

### Mihomo: native TUN

Mihomo не использует `xray-waybar-tun.service`: `tun.enable`, `auto-route`,
`auto-detect-interface` и DNS hijack включаются прямо в его runtime YAML.
Один раз выдай бинарнику минимальную capability:

```sh
sudo make install-mihomo-cap
```

После этого достаточно `core: mihomo` и `system_wide: true`. Pacman-hook из
этого target повторно применит capability после обновления пакета. Удаление:
`sudo make uninstall-mihomo-cap`.

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
  loop. Ловит две вещи: (1) выбранное ядро/API умерло → автореконнект,
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

Команда `xray-waybar-ctl menu` поднимает `walker --dmenu` со списком
серверов. Выбор сервера → `use`. В Xray режиме первой строкой также идёт
`Profile: …` → второй walker с вариантами `routing_profile`
(proxy-all / direct / smart / whitelist / custom URL). Запись идёт в
`state.json` и перебивает значение из `app.yaml` до
`xray-waybar-ctl profile reset`. В Mihomo режиме routing приходит из YAML,
поэтому Xray-подменю скрыто; provider-узлы появляются после первого `connect`.

Биндить на хоткей оконного менеджера или на `on-click` модуля Waybar.
`walker` должен быть в `$PATH` — иначе `menu` вернёт ошибку, остальной
CLI работает.

## Waybar

`~/.config/waybar/config.jsonc`:

```jsonc
"custom/vpn": {
    "exec": "xray-waybar-ctl status",
    "interval": 5,
    "return-type": "json",
    "on-click":        "xray-waybar-ctl toggle",
    "on-click-right":  "xray-waybar-ctl reconnect",
    "on-scroll-up":    "xray-waybar-ctl use-next",
    "on-scroll-down": "xray-waybar-ctl use-prev"
}
```

`~/.config/waybar/style.css`:

```css
#custom-vpn               { color: @disabled-color; padding: 0 8px; }
#custom-vpn.connected     { color: @green; }
#custom-vpn.loading       { color: @yellow; }
#custom-vpn.error         { color: @red; }
#custom-vpn.degraded      { color: @orange; }  /* system_wide=true но TUN не активен */
```

После правок: `pkill -SIGUSR2 waybar`.

## Разработка

```sh
make test       # юнит-тесты
make vet
make build      # выдаёт ./xray-waybar-ctl

# опционально: проверить сгенерированный YAML реальным ядром
MIHOMO_BIN=/usr/bin/mihomo go test ./internal/mihomo -run TestPreparedConfigAcceptedByMihomo -v
```

## Лицензия

MIT.

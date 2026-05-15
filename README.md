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

## Установка

```sh
# зависимости
yay -S xray waybar

# сборка и установка в ~/.local
make install
```

## Конфигурация

Скопируй пример и подставь свой URL подписки:

```sh
mkdir -p ~/.config/xray-waybar
cp ~/.local/share/xray-waybar/app.yaml.example ~/.config/xray-waybar/app.yaml
$EDITOR ~/.config/xray-waybar/app.yaml
```

Минимально нужен только `subscription_url`. Все остальные поля имеют разумные дефолты —
см. `configs/app.yaml.example`.

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
sudo make install-system    # systemd unit + polkit rule
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
```

## Лицензия

MIT.

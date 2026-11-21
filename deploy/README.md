# Стенд Music Quiz

Админка и игра доступны по адресу https://2.27.10.161. Caddy автоматически
выпускает и продлевает сертификат Let's Encrypt для IP. Для этого серверу
нужны входящие порты 80 и 443.

Пуш в `master` каждого репозитория запускает GitHub Actions. Backend проходит
тесты с PostgreSQL, frontend проходит ESLint и сборку TypeScript. Оба процесса
проверяют сборку Docker-образа. Таймеры systemd проверяют `master` примерно
раз в две минуты. После успешного CI сервер собирает образ проверенного
коммита и обновляет соответствующий сервис. Репозитории и результаты CI
читаются по HTTPS, поэтому ключи GitHub на сервере не требуются.

Сервер проверяет доступность сервиса и восстанавливает предыдущий образ
при ошибке. Миграции PostgreSQL применяются при запуске API, поэтому откат
образа требует совместимости со схемой после миграции. Сведения о проверках
и деплое доступны в `journalctl -u 'music-quiz-sync@*.service'`.

## Доступ к PostgreSQL

На компьютере с разрешённым SSH-ключом запустите и оставьте открытым:

```sh
ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:25432:127.0.0.1:15432 quiz-db@2.27.10.161
```

Подключите клиент PostgreSQL к `127.0.0.1:25432`, база и пользователь `quiz`.
Пароль берётся из `POSTGRES_PASSWORD` настроенного backend `.env`.

## Восстановление стенда на Ubuntu

1. Установите Docker Engine с Compose v2 и Buildx, Git, curl и Python 3.
   Создайте каталог `/srv/music-quiz` с владельцем `root` и перенесите туда
   `compose.yaml` и `Caddyfile` из этой папки. Если IP сервера изменился,
   обновите его в Caddyfile.
2. Поместите настроенный backend `.env` в `/srv/music-quiz/app.env` с правами
   `0600`. Создайте `media` с владельцем UID/GID `10001` и `images.env` с
   двумя строками: `API_REVISION=unpublished` и `FRONTEND_REVISION=unpublished`.
3. Установите `sync.sh` как `/usr/local/sbin/music-quiz-sync` от `root`
   с правами `0755`. Скопируйте `music-quiz-sync@.service` и
   `music-quiz-sync@.timer` в `/etc/systemd/system/`.
4. Для туннеля создайте пользователя `quiz-db` с ключом
   `restrict,port-forwarding,permitopen="127.0.0.1:15432"`. В sshd разрешите
   для этого пользователя только `AllowTcpForwarding local`, запретите
   `AllowStreamLocalForwarding` и назначьте `MaxSessions 0`.
5. Запустите БД и Caddy из `/srv/music-quiz` командой
   `sudo docker compose --env-file app.env --env-file images.env up -d --wait db proxy`.
   Выполните `sudo systemctl daemon-reload`, затем
   `sudo systemctl enable --now music-quiz-sync@api.timer music-quiz-sync@frontend.timer`.
   После первого успешного деплоя создайте администратора командой
   `create-admin` из контейнера API, если БД новая.
